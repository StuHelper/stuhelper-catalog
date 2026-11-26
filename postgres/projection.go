package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

//go:embed projection_source.sql
var projectionSourceSQL string

//go:embed projection_build.sql
var projectionBuildSQL string

func (t *transaction) touchProjection(school string) error {
	if t.projectionSchools[school] {
		return nil
	}
	if err := t.Lock("projection-source:" + school); err != nil {
		return err
	}
	var permitted bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT CASE WHEN $2='' THEN
 NOT EXISTS(SELECT 1 FROM catalog.projection_imports WHERE school_id=$1 AND state<>'complete')
 ELSE EXISTS(SELECT 1 FROM catalog.projection_imports WHERE school_id=$1 AND attempt_id=$2 AND state='running' AND deadline>clock_timestamp()) END`, school, c.DirectoryImportID(t.ctx)).Scan(&permitted); err != nil {
		return safe(err)
	}
	if !permitted {
		return c.ErrConflict
	}
	if t.projectionSchools == nil {
		t.projectionSchools = map[string]bool{}
	}
	t.projectionSchools[school] = true
	return nil
}

func (t *transaction) DirectoryProjection(school string) (c.ProjectionReport, error) {
	return read[c.ProjectionReport](t, `SELECT g.report FROM catalog.projection_heads h JOIN catalog.projection_generations g ON g.version=h.version WHERE h.school_id=$1`, school)
}

func (t *transaction) BeginDirectoryImport(school, batch, attempt string, at time.Time) error {
	if err := t.Lock("projection-source:" + school); err != nil {
		return err
	}
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_imports(school_id,batch,attempt_id,state,started_at,updated_at,deadline)
 VALUES($1,$2,$5,'running',$3,$3,$4) ON CONFLICT(school_id) DO UPDATE SET batch=excluded.batch,attempt_id=excluded.attempt_id,state='running',started_at=excluded.started_at,updated_at=excluded.updated_at,deadline=excluded.deadline
 WHERE catalog.projection_imports.state<>'running' OR catalog.projection_imports.deadline<=$3`, school, batch, at, at.Add(2*time.Hour), attempt)
	if err != nil {
		return safe(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if n != 1 {
		return c.ErrConflict
	}
	return nil
}

func (t *transaction) EndDirectoryImport(school, attempt string, success bool, at time.Time) error {
	if err := t.Lock("projection-source:" + school); err != nil {
		return err
	}
	state := "failed"
	if success {
		state = "complete"
	}
	result, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.projection_imports SET state=$3,updated_at=$4 WHERE school_id=$1 AND attempt_id=$2 AND state='running' AND ($3<>'complete' OR deadline>$4)`, school, attempt, state, at)
	if err != nil {
		return safe(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if n != 1 {
		return c.ErrConflict
	}
	return nil
}

func (t *transaction) SaveTermOrders(school string, terms []c.TermOrder) error {
	if err := t.touchProjection(school); err != nil {
		return err
	}
	raw, err := json.Marshal(terms)
	if err != nil {
		return c.ErrInput
	}
	var valid int
	if err = t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM jsonb_array_elements($1::jsonb) item
 JOIN catalog.objects o ON o.id=item->>'id' AND o.school_id=$2 AND o.kind='term'`, string(raw), school).Scan(&valid); err != nil {
		return safe(err)
	}
	if valid != len(terms) {
		return c.ErrInput
	}
	for _, term := range terms {
		if err := t.save(`INSERT INTO catalog.term_orders(school_id,id,sort_key,data) VALUES($1,$2,$3,$4)
 ON CONFLICT(id) DO UPDATE SET sort_key=excluded.sort_key,data=excluded.data WHERE catalog.term_orders.data IS DISTINCT FROM excluded.data`, term, school, term.ID, c.TermOrderKey(term.Year, term.Season)); err != nil {
			return err
		}
	}
	return nil
}

func (t *transaction) projectionVersion(school string) (int64, error) {
	var version int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT version FROM catalog.projection_heads WHERE school_id=$1`, school).Scan(&version)
	if err != nil {
		return 0, c.ErrUnavailable
	}
	return version, nil
}

func (t *transaction) projectionManaged(school string) (bool, error) {
	if t.repoDirectoryProjection || c.DirectoryRebuildDeferred(t.ctx) {
		return true, nil
	}
	if school == "" {
		return false, nil
	}
	if yes, ok := t.projectionManagedCache[school]; ok {
		return yes, nil
	}
	var exists bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)`, school).Scan(&exists); err != nil {
		return false, safe(err)
	}
	if t.projectionManagedCache == nil {
		t.projectionManagedCache = map[string]bool{}
	}
	t.projectionManagedCache[school] = exists
	return exists, nil
}

func (t *transaction) rebuildActive(school string) error {
	at := time.Now().UTC()
	if !t.mutationTime.IsZero() {
		at = t.mutationTime.UTC()
	}
	_, err := t.RebuildPublishedDirectory(school, at)
	return err
}

func (t *transaction) RebuildPublishedDirectory(school string, at time.Time) (*c.ProjectionReport, error) {
	if c.DirectoryRebuildDeferred(t.ctx) {
		return nil, nil
	}
	var exists bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)`, school).Scan(&exists); err != nil {
		return nil, safe(err)
	}
	if !exists {
		return nil, nil
	}
	out, err := t.RebuildDirectory(school, nil, at)
	return &out, err
}

func (t *transaction) flushProjection() error {
	for school := range t.projectionSchools {
		if c.MaintenanceRebuildDeferred(t.ctx) {
			if err := t.markProjectionPending(school); err != nil {
				return err
			}
			continue
		}
		if err := t.rebuildActive(school); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) recordProjectionFailure(school string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason := "build_failed"
	var df c.DirectoryFailure
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "build_timeout"
	} else if errors.Is(err, context.Canceled) {
		reason = "build_canceled"
	} else if errors.As(err, &df) && df.Reason == "lock_unavailable" {
		reason = "lock_timeout"
	}
	if _, e := r.DB.ExecContext(ctx, `INSERT INTO catalog.projection_failures(school_id,failed_at,reason) VALUES($1,now(),$2)
 ON CONFLICT(school_id) DO UPDATE SET failed_at=excluded.failed_at,reason=excluded.reason`, school, reason); e != nil {
		slog.Error("catalog projection failure could not be persisted", "reason", "database_unavailable")
	}
}

func (t *transaction) RebuildDirectory(school string, policy *c.DirectoryPolicy, at time.Time) (out c.ProjectionReport, err error) {
	started := time.Now()
	t.projectionAttempt = school
	if _, err = t.tx.ExecContext(t.ctx, `SET LOCAL statement_timeout='100s'; SET LOCAL lock_timeout='10s'; SET LOCAL jit=off`); err != nil {
		return out, safe(err)
	}
	if err = t.touchProjection(school); err != nil {
		return
	}
	if err = t.Lock("projection-build"); err != nil {
		return
	}
	var exists bool
	if err = t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE id=$1 AND school_id=$1 AND kind='school' AND NOT COALESCE((data->'object'->>'inactive')::boolean,false))`, school).Scan(&exists); err != nil {
		return out, safe(err)
	}
	if !exists {
		return out, c.ErrNotFound
	}
	if err = t.ensureMissingTermOrders(school); err != nil {
		return out, err
	}
	out.Policy = c.DefaultDirectoryPolicy()
	if policy == nil {
		var raw []byte
		e := t.tx.QueryRowContext(t.ctx, `SELECT policy FROM catalog.projection_heads WHERE school_id=$1`, school).Scan(&raw)
		if e == nil {
			if json.Unmarshal(raw, &out.Policy) != nil {
				return out, c.ErrUnavailable
			}
		} else if !errors.Is(safe(e), c.ErrNotFound) {
			return out, safe(e)
		}
	} else {
		out.Policy = *policy
	}
	if !out.Policy.Valid() {
		return out, c.ErrInput
	}
	if err = t.newProjectionGeneration(school, &out, at, nil, 0); err != nil {
		return out, err
	}
	for index := range projectionSteps {
		if err = t.buildProjectionStep(school, &out, index); err != nil {
			return out, err
		}
	}
	if err = t.prepareProjection(school, &out); err != nil {
		return out, err
	}
	if err = t.pruneProjection(school, out.Version); err != nil {
		return out, err
	}
	out.DurationMS = time.Since(started).Milliseconds()
	if err = t.publishProjection(school, &out, at); err != nil {
		return out, err
	}
	delete(t.projectionSchools, school)
	return out, nil
}

const projectionFormat = 1

var projectionSteps = strings.Split(projectionBuildSQL, "-- step\n")

func (t *transaction) ensureMissingTermOrders(school string) (err error) {
	if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.term_orders(school_id,id,sort_key,data)
 SELECT school_id,id,0,jsonb_build_object('id',id,'year',0,'season','','reason','missing_term_order') FROM catalog.objects
 WHERE school_id=$1 AND kind='term' ON CONFLICT(id) DO NOTHING`, school); err != nil {
		return safe(err)
	}
	return nil
}

func (t *transaction) newProjectionGeneration(school string, out *c.ProjectionReport, at time.Time, step *int, base int64) (err error) {
	out.BuiltAt = at
	if err = t.tx.QueryRowContext(t.ctx, `SELECT GREATEST($1::bigint,COALESCE(max(version),0)+1) FROM catalog.projection_generations`, at.UnixMilli()).Scan(&out.Version); err != nil {
		return safe(err)
	}
	raw, _ := json.Marshal(out.Policy)
	if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_generations(version,school_id,policy,source_revision,built_at,format_version,build_step,build_base)
 SELECT $1,$2,$3::jsonb,revision,$4,$5,$6,$7 FROM catalog.directory_revision WHERE singleton`, out.Version, school, string(raw), at, projectionFormat, step, base); err != nil {
		return safe(err)
	}
	return nil
}

func (t *transaction) buildProjectionStep(school string, out *c.ProjectionReport, index int) (err error) {
	step := projectionSteps[index]
	stepStarted := time.Now()
	step = strings.TrimSpace(step)
	prefix := `WITH _projection_args AS (SELECT $1::text AS school,$2::bigint AS version,$3::integer AS cutoff) `
	if strings.Contains(step, "FROM projection_source ") {
		prefix += ", projection_source AS NOT MATERIALIZED (" + projectionSourceSQL + ") "
	}
	if strings.HasPrefix(step, "WITH ") {
		step = "," + strings.TrimPrefix(step, "WITH ")
	}
	if _, e := t.tx.ExecContext(t.ctx, prefix+step, school, out.Version, c.TermOrderKey(out.Policy.CutoffYear, out.Policy.CutoffSeason)); e != nil {
		slog.ErrorContext(t.ctx, "catalog projection build failed", "step", index+1, "reason", projectionErrorCode(e))
		return safe(e)
	}
	if index == 0 {
		// Department objects belong to the first build step. Assign their
		// ranks before constructing facets and the cached default pages.
		if _, err := t.tx.ExecContext(t.ctx, "SELECT catalog.rank_projection_departments($1)", out.Version); err != nil {
			return safe(err)
		}
	}
	// The child rows inherit the final class mapping. Apply school rules
	// once to classes before materializing their assignments and schedules.
	if index == 5 {
		// Sports and category rules join against the classes just inserted.
		// Analyze them before planning those joins, including on the first build.
		if _, err := t.tx.ExecContext(t.ctx, `SELECT catalog.analyze_directory_projection()`); err != nil {
			return safe(err)
		}
		if e := t.applyDisplayPolicy(school, out); e != nil {
			return e
		}
	}
	if duration := time.Since(stepStarted); duration > 300*time.Millisecond {
		slog.InfoContext(t.ctx, "catalog projection build stage", "step", index+1, "duration_ms", duration.Milliseconds())
	}
	// New generation values have no statistics until this transaction builds
	// them. Refresh after bulk phases to avoid repeated full-scan join plans.
	switch index + 1 {
	case 1, 2, 6, 7, 9, 12, 17:
		if _, e := t.tx.ExecContext(t.ctx, `SELECT catalog.analyze_directory_projection()`); e != nil {
			return safe(e)
		}
	}
	return nil
}

func (t *transaction) prepareProjection(school string, out *c.ProjectionReport) (err error) {
	// Keep category objects and course membership available to detail reads.
	// Only the published filter options are suppressed.
	if len(out.Policy.HiddenCategoryNames) > 0 {
		if _, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.projection_options
 WHERE version=$1 AND owner_kind='course' AND kind='category' AND lower(btrim(name))=ANY($2::text[])`, out.Version, normalizedNames(out.Policy.HiddenCategoryNames)); err != nil {
			return safe(err)
		}
	}
	if err = t.validateProjection(school, out); err != nil {
		return err
	}
	for _, kind := range []string{"course", "teacher"} {
		query, args, e := projectedSelectionQuery(c.DirectoryQuery{Filter: c.Filter{Kind: kind, SchoolID: school}}, out.Version, false)
		if e != nil {
			return e
		}
		if _, e = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_selections(version,kind,data) SELECT $1,$2,selected.data FROM (`+query+`) selected(data)`, args...); e != nil {
			return safe(e)
		}
	}
	var selectionsValid bool
	if err = t.tx.QueryRowContext(t.ctx, `SELECT count(*)=2 AND bool_and(octet_length(data::text)<=33554432)
 FROM catalog.projection_selections WHERE version=$1`, out.Version).Scan(&selectionsValid); err != nil {
		return safe(err)
	}
	if !selectionsValid {
		return c.ErrUnavailable
	}
	if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_documents(version,id,data)
 SELECT $2,latest.id,latest.data || jsonb_build_object('revision',$2,'title','','visible',false,'code','','aliases','[]'::jsonb,'english_name','','former_codes','[]'::jsonb,'former_names','[]'::jsonb,'at',g.built_at)
 FROM (SELECT DISTINCT ON (h.id) h.id,h.data FROM catalog.discovery_history h JOIN catalog.objects o ON o.id=h.id WHERE o.school_id=$1 ORDER BY h.id,h.revision DESC) latest
 JOIN catalog.projection_generations g ON g.version=$2
 WHERE NOT EXISTS(SELECT 1 FROM catalog.projection_heads ph WHERE ph.school_id=$1)
   AND EXISTS (SELECT 1 FROM catalog.outbox e WHERE e.data->>'type'='catalog.discovery.v1' AND e.data->>'subject'=latest.id AND (e.data->'data'->>'visible')::boolean)
   AND (latest.data->>'visible')::boolean
   AND NOT EXISTS(SELECT 1 FROM catalog.projection_documents n WHERE n.version=$2 AND n.id=latest.id)`, school, out.Version); err != nil {
		return safe(err)
	}
	if err = t.tx.QueryRowContext(t.ctx, `SELECT
 (SELECT count(*) FROM catalog.projection_relations WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_teaching_history WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_summaries WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_documents WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_aliases WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_selections WHERE version=$1)`, out.Version).Scan(&out.Relations, &out.TeachingHistory, &out.Summaries, &out.Documents, &out.Aliases, &out.Selections); err != nil {
		return safe(err)
	}
	out.Rows = out.SportsClasses + out.SportsSourceCourses + out.Objects + out.Variants + out.Relations + out.TeachingHistory + out.Options + out.Summaries + out.Documents + out.Aliases + out.Selections
	var selectionBytes int64
	if err = t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(sum(pg_column_size(s)),0) FROM catalog.projection_selections s WHERE version=$1`, out.Version).Scan(&selectionBytes); err != nil {
		return safe(err)
	}
	out.Bytes += selectionBytes
	if err = t.tx.QueryRowContext(t.ctx, `SELECT sum(pg_total_relation_size(('catalog.'||name)::regclass)) FROM
 unnest(ARRAY['projection_generations','projection_heads','projection_objects','projection_sports_sources','projection_sports_classes','projection_variants','projection_relations','projection_teaching_history','projection_options','projection_aliases','projection_summaries','projection_documents','projection_selections']) name`).Scan(&out.StorageBytes); err != nil {
		return safe(err)
	}
	return nil
}

func (t *transaction) pruneProjection(school string, version int64) (err error) {
	// Keep the candidate and the latest other published generation. Drafts
	// have separate ownership and are invisible to published reads.
	_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.projection_generations WHERE school_id=$1 AND build_step IS NULL
 AND version<>$2 AND version NOT IN (SELECT version FROM catalog.projection_heads WHERE school_id=$1)`, school, version)
	if err != nil {
		return safe(err)
	}
	_, err = t.tx.ExecContext(t.ctx, `SELECT catalog.analyze_directory_projection()`)
	return safe(err)
}

func (t *transaction) publishProjection(school string, out *c.ProjectionReport, at time.Time) (err error) {
	if err = t.publishProjectionChanges(school, out, at); err != nil {
		return err
	}
	return t.publishProjectionHead(school, out)
}

func (t *transaction) publishProjectionChanges(school string, out *c.ProjectionReport, at time.Time) (err error) {
	if err = t.publishProjectionDocuments(school, out.Version, at); err != nil {
		return err
	}
	if err = t.syncProjectedActivityVisibility(school, out.Version, at); err != nil {
		return err
	}
	return nil
}

func (t *transaction) publishProjectionHead(school string, out *c.ProjectionReport) (err error) {
	raw, err := json.Marshal(out.Policy)
	if err != nil {
		return c.ErrInput
	}
	if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.projection_heads(school_id,version,policy) VALUES($1,$2,$3::jsonb)
 ON CONFLICT(school_id) DO UPDATE SET version=excluded.version,policy=excluded.policy`, school, out.Version, string(raw)); err != nil {
		return safe(err)
	}
	if t.projectionManagedCache == nil {
		t.projectionManagedCache = map[string]bool{}
	}
	t.projectionManagedCache[school] = true
	if _, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.projection_failures WHERE school_id=$1`, school); err != nil {
		return safe(err)
	}
	return t.save(`UPDATE catalog.projection_generations SET report=$2,build_step=NULL WHERE version=$1`, out, out.Version)
}

func projectionErrorCode(err error) string {
	// SQLSTATE is safe diagnostic data; query values and database messages are not.
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return "database_unavailable"
}

func (t *transaction) validateProjection(school string, out *c.ProjectionReport) error {
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*),count(*) FILTER(WHERE kind='course' AND visible),count(*) FILTER(WHERE kind='teacher' AND visible),
 count(*) FILTER(WHERE kind='teacher' AND teaching),count(*) FILTER(WHERE kind='teacher' AND official),count(*) FILTER(WHERE kind='teacher' AND teaching AND official),
 COALESCE(sum(pg_column_size(o)),0) FROM catalog.projection_objects o WHERE version=$1`, out.Version).Scan(&out.Objects, &out.Courses, &out.Teachers, &out.Marks.Teaching, &out.Marks.Official, &out.Marks.Both, &out.Bytes)
	if err != nil {
		return safe(err)
	}
	out.Marks.Eligible = int(out.Teachers)
	if out.Objects > 1000000 || out.Courses > 100000 || out.Teachers > 100000 {
		return c.ErrUnavailable
	}
	var extra int64
	err = t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(sum(bytes),0) FROM (
 SELECT pg_column_size(v) AS bytes FROM catalog.projection_variants v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_relations v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_teaching_history v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_options v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_aliases v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_summaries v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_documents v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_sports_sources v WHERE version=$1
 UNION ALL SELECT pg_column_size(v) FROM catalog.projection_sports_classes v WHERE version=$1) sizes`, out.Version).Scan(&extra)
	if err != nil {
		return safe(err)
	}
	out.Bytes += extra
	err = t.tx.QueryRowContext(t.ctx, `SELECT (SELECT count(*) FROM catalog.projection_variants WHERE version=$1),
 (SELECT count(*) FROM catalog.projection_options WHERE version=$1),
 (SELECT count(*) FROM catalog.objects o LEFT JOIN catalog.term_orders tr ON tr.id=o.id AND tr.school_id=o.school_id
 WHERE o.school_id=$2 AND o.kind='term' AND COALESCE(tr.sort_key,0)=0)`, out.Version, school).Scan(&out.Variants, &out.Options, &out.InvalidTerms)
	if err != nil {
		return safe(err)
	}
	if out.Variants > 500000 {
		return c.ErrUnavailable
	}
	var valid bool
	err = t.tx.QueryRowContext(t.ctx, `SELECT NOT EXISTS(SELECT 1 FROM catalog.projection_objects o WHERE o.version=$1 AND o.visible AND
 ((o.kind='course' AND NOT EXISTS(SELECT 1 FROM catalog.projection_variants v WHERE v.version=$1 AND v.course_id=o.id AND v.offering_id<>''))
 OR (o.kind='teacher' AND NOT(o.teaching OR o.official OR o.verified_homepage)))) AND
 NOT EXISTS(SELECT 1 FROM catalog.projection_variants v WHERE v.version=$1 AND v.offering_id<>'' AND v.term_key<$2)`, out.Version, c.TermOrderKey(out.Policy.CutoffYear, out.Policy.CutoffSeason)).Scan(&valid)
	if err != nil {
		return safe(err)
	}
	if !valid {
		return c.ErrUnavailable
	}
	return nil
}

func (t *transaction) publishProjectionDocuments(school string, version int64, at time.Time) error {
	// Unchanged documents retain their search revision. A visibility or title
	// change receives the generation revision and its outbox event atomically.
	_, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.projection_documents n SET data=old.data
 FROM catalog.projection_heads h JOIN catalog.projection_documents old ON old.version=h.version
 WHERE h.school_id=$1 AND n.version=$2 AND n.id=old.id AND (n.data-'revision'-'at')=(old.data-'revision'-'at')`, school, version)
	if err != nil {
		return safe(err)
	}
	after := ""
	for {
		docs, err := list[c.DiscoveryDocument](t, `SELECT data FROM catalog.projection_documents WHERE version=$1 AND (data->>'revision')::bigint=$1 AND id>$2 ORDER BY id LIMIT 500`, version, after)
		if err != nil {
			return err
		}
		if len(docs) == 0 {
			break
		}
		for _, d := range docs {
			id := fmt.Sprintf("projection:%d:%s", version, d.Target.ID)
			if err := t.saveEnvelope(c.Envelope{SpecVersion: "1.0", ID: id, Source: "catalog", Type: "catalog.discovery.v1", Subject: d.Target.ID, Time: at,
				Data: c.ResultData{EnglishName: d.EnglishName, FormerCodes: d.FormerCodes, FormerNames: d.FormerNames, Revision: d.Revision, Target: &d.Target, Title: &d.Title, SchoolID: &d.SchoolID, Visible: &d.Visible, Code: d.Code, Aliases: d.Aliases, Ratings: &d.Ratings, At: &d.At}}); err != nil {
				return err
			}
		}
		after = docs[len(docs)-1].Target.ID
	}
	return nil
}
