package postgres

import (
	"database/sql"
	"errors"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (t *transaction) RecordProjectionAudit(v c.Event) error {
	return t.recordAudit(v)
}

func (t *transaction) expireProjectionRebuild(school string, now time.Time) (*c.ProjectionRebuildRun, error) {
	v, err := read[c.ProjectionRebuildRun](t, `SELECT data FROM catalog.projection_rebuild_runs WHERE school_id=$1 AND status IN ('queued','running') LIMIT 1`, school)
	if errors.Is(err, c.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now.Before(v.DeadlineAt) {
		return &v, nil
	}
	v.Status = "timed_out"
	v.FailureReason = "timeout"
	v.FinishedAt = &now
	if v.StartedAt != nil {
		v.DurationMS = max(0, now.Sub(*v.StartedAt).Milliseconds())
	}
	if err := t.save(`UPDATE catalog.projection_rebuild_runs SET status=$2,data=$3 WHERE id=$1`, v, v.ID, v.Status); err != nil {
		return nil, err
	}
	if err := t.recordAudit(c.Event{
		ID:       "projection-timeout:" + v.ID,
		Subject:  v.ID,
		Type:     "catalog.directory-projection.finished.v1",
		SchoolID: school,
		Actor:    "urn:stuhelper:maintenance:directory-projection",
		Result:   "failed",
		Time:     now,
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (t *transaction) ExpireDirectoryRebuildIfNeeded(school string, now time.Time) error {
	var expired bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_rebuild_runs WHERE school_id=$1 AND status IN ('queued','running') AND (data->>'deadline_at')::timestamptz<=$2)`, school, now).Scan(&expired); err != nil {
		return safe(err)
	}
	if !expired {
		return nil
	}
	if err := t.Lock("projection-rebuild:" + school); err != nil {
		return err
	}
	_, err := t.expireProjectionRebuild(school, now)
	return err
}

func (t *transaction) DirectoryProjectionStatus(school string, _ time.Time) (c.DirectoryProjectionStatus, error) {
	var out c.DirectoryProjectionStatus
	if _, err := t.tx.ExecContext(t.ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return out, safe(err)
	}
	var exists bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE id=$1 AND school_id=$1 AND kind='school' AND NOT COALESCE((data->'object'->>'inactive')::boolean,false))`, school).Scan(&exists); err != nil {
		return out, safe(err)
	}
	if !exists {
		return out, c.ErrNotFound
	}
	if err := t.tx.QueryRowContext(t.ctx, `SELECT revision FROM catalog.directory_revision WHERE singleton`).Scan(&out.SourceRevision); err != nil {
		return out, safe(err)
	}
	report, err := t.DirectoryProjection(school)
	if err == nil {
		out.Published = &report
		if err := t.tx.QueryRowContext(t.ctx, `SELECT source_revision FROM catalog.projection_generations WHERE version=$1`, report.Version).Scan(&out.PublishedRevision); err != nil {
			return out, safe(err)
		}
	} else if !errors.Is(err, c.ErrNotFound) {
		return out, err
	}
	if out.SourceRevision > out.PublishedRevision {
		out.SourceRevisionLag = out.SourceRevision - out.PublishedRevision
	}
	var reason string
	var failedAt time.Time
	err = t.tx.QueryRowContext(t.ctx, `SELECT reason,failed_at FROM catalog.projection_failures WHERE school_id=$1`, school).Scan(&reason, &failedAt)
	if err == nil {
		out.LatestFailure = &c.ProjectionFailure{Reason: reason, FailedAt: failedAt.UTC()}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, safe(err)
	}
	latest, err := read[c.ProjectionRebuildRun](t, `SELECT data FROM catalog.projection_rebuild_runs WHERE school_id=$1 ORDER BY position DESC LIMIT 1`, school)
	if err == nil {
		out.LatestRun = &latest
	} else if !errors.Is(err, c.ErrNotFound) {
		return out, err
	}
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_pending WHERE school_id=$1) OR EXISTS(SELECT 1 FROM catalog.projection_rebuild_runs WHERE school_id=$1 AND status IN ('queued','running'))`, school).Scan(&out.PendingRebuild); err != nil {
		return out, safe(err)
	}
	return out, nil
}

func (t *transaction) QueueDirectoryRebuild(run c.ProjectionRebuildRun, policyDigest, _ string, now time.Time) (c.ProjectionRebuildRun, error) {
	if err := t.Lock("projection-rebuild:" + run.SchoolID); err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	active, err := t.expireProjectionRebuild(run.SchoolID, now)
	if err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	var existingDigest string
	prior, err := read[c.ProjectionRebuildRun](t, `SELECT data FROM catalog.projection_rebuild_runs WHERE id=$1`, run.ID)
	if err == nil {
		if err := t.tx.QueryRowContext(t.ctx, `SELECT policy_digest FROM catalog.projection_rebuild_runs WHERE id=$1`, run.ID).Scan(&existingDigest); err != nil {
			return c.ProjectionRebuildRun{}, safe(err)
		}
		if existingDigest != policyDigest {
			return c.ProjectionRebuildRun{}, c.ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, c.ErrNotFound) {
		return c.ProjectionRebuildRun{}, err
	}
	var exists bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE id=$1 AND school_id=$1 AND kind='school' AND NOT COALESCE((data->'object'->>'inactive')::boolean,false))`, run.SchoolID).Scan(&exists); err != nil {
		return c.ProjectionRebuildRun{}, safe(err)
	}
	if !exists {
		return c.ProjectionRebuildRun{}, c.ErrNotFound
	}
	var fenced bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_imports WHERE school_id=$1 AND state<>'complete')`, run.SchoolID).Scan(&fenced); err != nil {
		return c.ProjectionRebuildRun{}, safe(err)
	}
	if active != nil || fenced {
		return c.ProjectionRebuildRun{}, c.ErrConflict
	}
	var full bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_rebuild_runs OFFSET 9999)`).Scan(&full); err != nil {
		return c.ProjectionRebuildRun{}, safe(err)
	}
	if full {
		return c.ProjectionRebuildRun{}, c.ErrUnavailable
	}
	if err := t.save(`INSERT INTO catalog.projection_rebuild_runs(id,school_id,status,requested_at,policy_digest,data) VALUES($1,$2,$3,$4,$5,$6)`, run, run.ID, run.SchoolID, run.Status, run.RequestedAt, policyDigest); err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	if _, err := t.tx.ExecContext(t.ctx, `SELECT pg_notify($1,$2)`, c.ProjectionChannel, run.SchoolID); err != nil {
		return c.ProjectionRebuildRun{}, safe(err)
	}
	return run, nil
}

func (t *transaction) ClaimDirectoryRebuild(school, actor string, now time.Time) (*c.ProjectionRebuildRun, error) {
	if err := t.Lock("projection-rebuild:" + school); err != nil {
		return nil, err
	}
	active, err := t.expireProjectionRebuild(school, now)
	if err != nil || active == nil || active.Status != "queued" {
		return nil, err
	}
	active.Status = "running"
	active.StartedAt = &now
	if err := t.save(`UPDATE catalog.projection_rebuild_runs SET status=$2,data=$3 WHERE id=$1`, *active, active.ID, active.Status); err != nil {
		return nil, err
	}
	if err := t.recordAudit(c.Event{
		ID:       "projection-claim:" + active.ID,
		Subject:  active.ID,
		Type:     "catalog.directory-projection.started.v1",
		SchoolID: school,
		Actor:    actor,
		Result:   "success",
		Time:     now,
	}); err != nil {
		return nil, err
	}
	return active, nil
}

func (t *transaction) FinishDirectoryRebuild(id string, version *int64, failureReason, actor string, now time.Time) (c.ProjectionRebuildRun, error) {
	run, err := read[c.ProjectionRebuildRun](t, `SELECT data FROM catalog.projection_rebuild_runs WHERE id=$1`, id)
	if err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	if err := t.Lock("projection-rebuild:" + run.SchoolID); err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	run, err = read[c.ProjectionRebuildRun](t, `SELECT data FROM catalog.projection_rebuild_runs WHERE id=$1`, id)
	if err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	if run.Status != "running" {
		return c.ProjectionRebuildRun{}, c.ErrConflict
	}
	run.FinishedAt = &now
	if run.StartedAt != nil {
		run.DurationMS = max(0, now.Sub(*run.StartedAt).Milliseconds())
	}
	run.Version = version
	run.FailureReason = failureReason
	run.Status = "succeeded"
	auditResult := "success"
	if failureReason != "" {
		run.Status = "failed"
		auditResult = "failed"
		if failureReason == "timeout" || failureReason == "build_timeout" {
			run.Status = "timed_out"
		}
	}
	if err := t.save(`UPDATE catalog.projection_rebuild_runs SET status=$2,data=$3 WHERE id=$1`, run, run.ID, run.Status); err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	if err := t.recordAudit(c.Event{
		ID:       "projection-finish:" + run.ID,
		Subject:  run.ID,
		Type:     "catalog.directory-projection.finished.v1",
		SchoolID: run.SchoolID,
		Actor:    actor,
		Result:   auditResult,
		Time:     now,
	}); err != nil {
		return c.ProjectionRebuildRun{}, err
	}
	return run, nil
}

func (t *transaction) PendingProjectionUpdatedAt(school string) (*time.Time, error) {
	var updated time.Time
	err := t.tx.QueryRowContext(t.ctx, `SELECT updated_at FROM catalog.projection_pending WHERE school_id=$1`, school).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, safe(err)
	}
	u := updated.UTC()
	return &u, nil
}

func (t *transaction) ClearPendingProjection(school string, upTo time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.projection_pending WHERE school_id=$1 AND updated_at<=$2`, school, upTo)
	return safe(err)
}

func (t *transaction) markProjectionPending(school string) error {
	var published bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)`, school).Scan(&published); err != nil {
		return safe(err)
	}
	if !published {
		return nil
	}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_pending(school_id,requested_at,updated_at) VALUES($1,clock_timestamp(),clock_timestamp())
 ON CONFLICT(school_id) DO UPDATE SET updated_at=excluded.updated_at`, school); err != nil {
		return safe(err)
	}
	_, err := t.tx.ExecContext(t.ctx, `SELECT pg_notify($1,$2)`, c.ProjectionChannel, school)
	return safe(err)
}
