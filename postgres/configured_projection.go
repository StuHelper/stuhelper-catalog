package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

type projectionInput struct {
	head, revision int64
	format         int
	policy         c.DirectoryPolicy
}

func (t *transaction) projectionInput(school string) (projectionInput, error) {
	var v projectionInput
	var policy []byte
	var fenced bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(h.version,0),COALESCE(g.format_version,0),COALESCE(h.policy,'{}'::jsonb),r.revision,
 EXISTS(SELECT FROM catalog.projection_imports i WHERE i.school_id=o.id AND i.state<>'complete')
 FROM catalog.objects o CROSS JOIN catalog.directory_revision r LEFT JOIN catalog.projection_heads h ON h.school_id=o.id
 LEFT JOIN catalog.projection_generations g ON g.version=h.version
 WHERE r.singleton AND o.id=$1 AND o.school_id=$1 AND o.kind='school' AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)`, school).Scan(&v.head, &v.format, &policy, &v.revision, &fenced)
	if err != nil {
		return v, safe(err)
	}
	if fenced {
		return v, c.ErrConflict
	}
	if json.Unmarshal(policy, &v.policy) != nil {
		return v, c.ErrUnavailable
	}
	return v, nil
}
func (t *transaction) tryProjectionLock(key string) error {
	var locked bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "catalog:"+key).Scan(&locked); err != nil {
		return safe(err)
	}
	if !locked {
		return c.ErrConflict
	}
	return nil
}
func (t *transaction) AdvanceConfiguredProjection(school string, policy c.DirectoryPolicy, at time.Time, actor string) (int64, int, bool, error) {
	if _, err := t.tx.ExecContext(t.ctx, `SET LOCAL statement_timeout='100s'; SET LOCAL lock_timeout='1s'; SET LOCAL jit=off`); err != nil {
		return 0, 0, false, safe(err)
	}
	if err := t.tryProjectionLock("configured-projection:" + school); err != nil {
		return 0, 0, false, err
	}
	input, err := t.projectionInput(school)
	if err != nil {
		return 0, 0, false, err
	}
	var version, base, revision int64
	var step, format int
	var raw []byte
	err = t.tx.QueryRowContext(t.ctx, `SELECT version,build_base,source_revision,format_version,build_step,report
 FROM catalog.projection_generations WHERE school_id=$1 AND build_step IS NOT NULL`, school).Scan(&version, &base, &revision, &format, &step, &raw)
	var report c.ProjectionReport
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if input.head != 0 && input.format == projectionFormat && input.policy.Equal(policy) {
			return input.head, 0, true, nil
		}
		if err = t.tryProjectionLock("projection-build"); err != nil {
			return 0, 0, false, err
		}
		if err = t.ensureMissingTermOrders(school); err != nil {
			return 0, 0, false, err
		}
		report.Policy = policy
		if err = t.newProjectionGeneration(school, &report, at, &step, input.head); err != nil {
			return 0, 0, false, err
		}
		err = t.save(`UPDATE catalog.projection_generations SET report=$2 WHERE version=$1`, report, report.Version)
		return report.Version, 0, false, err
	case err != nil:
		return 0, 0, false, safe(err)
	}
	if json.Unmarshal(raw, &report) != nil || report.Version != version {
		return version, step, false, c.ErrUnavailable
	}
	if input.revision != revision || input.head != base || format != projectionFormat || !policy.Equal(report.Policy) {
		_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.projection_generations WHERE version=$1 AND build_step IS NOT NULL`, version)
		return 0, 0, false, safe(err)
	}
	if step < 0 || step > len(projectionSteps)+1 {
		return version, step, false, c.ErrUnavailable
	}
	t.projectionAttempt = school
	switch {
	case step < len(projectionSteps):
		err = t.buildProjectionStep(school, &report, step)
	case step == len(projectionSteps):
		err = t.prepareProjection(school, &report)
	default:
		// Do the large immutable-generation cleanup before attempting the short
		// source/publication fence. A concurrent writer makes this stage retry.
		if err = t.pruneProjection(school, version); err != nil {
			break
		}
		if err = t.publishProjectionChanges(school, &report, at); err != nil {
			break
		}
		if err = t.tryProjectionLock("projection-build"); err != nil {
			break
		}
		if err = t.tryProjectionLock("projection-source:" + school); err != nil {
			break
		}
		current, e := t.projectionInput(school)
		if e != nil {
			err = e
			break
		}
		if current.head != base || current.revision != revision {
			err = c.ErrConflict
			break
		}
		report.DurationMS = at.Sub(report.BuiltAt).Milliseconds()
		if err = t.publishProjectionHead(school, &report); err != nil {
			break
		}
		err = t.Event(c.Event{ID: "configured-projection:" + strconv.FormatInt(version, 10), Subject: "school/" + school, Type: "catalog.directory.rebuilt.v1", SchoolID: school, Actor: actor, Result: "success", Revision: version, Time: at})
		return version, step + 1, err == nil, err
	}
	if err != nil {
		return version, step, false, err
	}
	step++
	err = t.save(`UPDATE catalog.projection_generations SET build_step=$2,report=$3 WHERE version=$1`, report, version, step)
	return version, step, false, err
}

func (t *transaction) ConfiguredProjectionCurrent(school string, policy c.DirectoryPolicy) (bool, error) {
	var current bool
	raw, err := json.Marshal(policy)
	if err != nil {
		return false, c.ErrInput
	}
	err = t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT FROM catalog.projection_heads h JOIN catalog.projection_generations g ON g.version=h.version
 WHERE h.school_id=$1 AND g.format_version=$2 AND h.policy=$3::jsonb)
 AND NOT EXISTS(SELECT FROM catalog.projection_generations WHERE school_id=$1 AND build_step IS NOT NULL)`, school, projectionFormat, string(raw)).Scan(&current)
	return current, safe(err)
}
