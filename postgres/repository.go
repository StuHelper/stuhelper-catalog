// Package postgres is the sole owner of catalog SQL and migrations.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	catalog "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var baseSchema string

//go:embed course_search.sql
var courseSearchSchema string

//go:embed activity_calendar.sql
var activityCalendarSchema string

//go:embed teacher_homepage_requests.sql
var teacherHomepageRequestSchema string

var Schema = baseSchema + "\n" + courseSearchSchema + "\n" + activityCalendarSchema + "\n" + teacherHomepageRequestSchema

type Repository struct {
	// TeacherImageStorage overrides byte storage; nil uses the existing PostgreSQL rows.
	TeacherImageStorage catalog.TeacherImageStorage
	DB                  *sql.DB
	// Enable in product composition. Maintenance candidate enumeration has its
	// own authorized port and does not disable this public read filter.
	RequireTeacherDirectory    bool
	RequireDirectoryProjection bool
	directoryOnce              sync.Once
	directoryData              *directoryCache
}
type transaction struct {
	imageRegistryDB         *sql.DB
	teacherImageStorage     catalog.TeacherImageStorage
	projectionRead          bool
	repoDirectoryProjection bool
	projectionSchools       map[string]bool
	projectionManagedCache  map[string]bool
	projectionAttempt       string
	directoryData           *directoryCache
	requireTeacherDirectory bool
	repoTeacherDirectory    bool
	pendingTeachers         map[string]bool
	pendingTeacherSchools   map[string]bool
	teachingTime            time.Time
	mutationTime            time.Time
	bodyImageQuotaExceeded  bool
	tx                      *sql.Tx
	ctx                     context.Context
	objects                 map[string]catalog.Record
}

func safe(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, sql.ErrNoRows) {
		return catalog.ErrNotFound
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return e
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) {
		switch pg.Code {
		case "23505", "40001", "40P01":
			return catalog.ErrConflict
		case "57014":
			return catalog.DirectoryFailure{Reason: "query_canceled"}
		case "53300":
			return catalog.DirectoryFailure{Reason: "connections_exhausted"}
		case "55P03":
			return catalog.DirectoryFailure{Reason: "lock_unavailable"}
		case "08000", "08003", "08006", "57P01", "57P02", "57P03":
			return catalog.DirectoryFailure{Reason: "database_unavailable"}
		}
	}
	return catalog.ErrUnavailable
}
func (r *Repository) Transaction(ctx context.Context, run func(catalog.Transaction) error) error {
	start := time.Now()
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil || time.Since(start) >= 300*time.Millisecond {
		stats := r.DB.Stats()
		reason := "slow"
		if errors.Is(e, context.DeadlineExceeded) {
			reason = "deadline_exceeded"
		} else if errors.Is(e, context.Canceled) {
			reason = "canceled"
		} else if e != nil {
			reason = "database_unavailable"
		}
		slog.WarnContext(ctx, "catalog database transaction begin", "reason", reason, "duration_ms", time.Since(start).Milliseconds(), "pool_in_use", stats.InUse, "pool_max_open", stats.MaxOpenConnections, "pool_wait_count", stats.WaitCount)
	}
	if e != nil {
		return safe(e)
	}
	defer tx.Rollback()
	t := &transaction{
		imageRegistryDB:         r.DB,
		teacherImageStorage:     r.TeacherImageStorage,
		tx:                      tx,
		ctx:                     ctx,
		objects:                 map[string]catalog.Record{},
		requireTeacherDirectory: r.RequireTeacherDirectory && !catalog.TeacherMaintenanceRead(ctx),
		repoTeacherDirectory:    r.RequireTeacherDirectory,
		directoryData:           r.directoryCache(),
		projectionRead:          r.RequireDirectoryProjection && !catalog.TeacherMaintenanceRead(ctx),
		repoDirectoryProjection: r.RequireDirectoryProjection,
	}
	if e = run(t); e != nil {
		if t.projectionAttempt != "" {
			_ = tx.Rollback()
			r.recordProjectionFailure(t.projectionAttempt, e)
		}
		return e
	}
	if e = t.flushTeachingDirectory(); e != nil {
		return e
	}
	if e = t.flushProjection(); e != nil {
		_ = tx.Rollback()
		r.recordProjectionFailure(t.projectionAttempt, e)
		return e
	}
	e = tx.Commit()
	if e != nil && t.projectionAttempt != "" {
		r.recordProjectionFailure(t.projectionAttempt, e)
	}
	return safe(e)
}
func (t *transaction) Lock(key string) error {
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "catalog:"+key)
	if e == nil {
		clear(t.objects)
	}
	return safe(e)
}
func read[T any](t *transaction, q string, args ...any) (T, error) {
	var v T
	var b []byte
	if e := t.tx.QueryRowContext(t.ctx, q, args...).Scan(&b); e != nil {
		return v, safe(e)
	}
	if json.Unmarshal(b, &v) != nil {
		return v, catalog.ErrUnavailable
	}
	return v, nil
}
func list[T any](t *transaction, q string, args ...any) ([]T, error) {
	rows, e := t.tx.QueryContext(t.ctx, q, args...)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var v T
		if rows.Scan(&b) != nil || json.Unmarshal(b, &v) != nil {
			return nil, catalog.ErrUnavailable
		}
		out = append(out, v)
	}
	return out, safe(rows.Err())
}
func (t *transaction) save(q string, v any, args ...any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return catalog.ErrInput
	}
	args = append(args, string(b))
	_, e = t.tx.ExecContext(t.ctx, q, args...)
	return safe(e)
}
func (t *transaction) Object(id string) (catalog.Record, error) {
	if r, ok := t.objects[id]; ok {
		return r, nil
	}
	r, err := read[catalog.Record](t, `SELECT data FROM catalog.objects WHERE id=$1`, id)
	if err == nil && len(t.objects) < 10000 {
		if t.objects == nil {
			t.objects = map[string]catalog.Record{}
		}
		t.objects[id] = r
	}
	return r, err
}
func (t *transaction) SaveObject(r catalog.Record) error {
	o := r.Object
	if o.TeacherHomepage != nil || r.Imported.TeacherHomepage != nil {
		return catalog.ErrInput
	}
	if !o.UpdatedAt.IsZero() && o.UpdatedAt.After(t.mutationTime) {
		t.mutationTime = o.UpdatedAt
	}
	if e := t.touchProjection(o.SchoolID); e != nil {
		return e
	}
	if o.Kind == "teacher" || o.Kind == "assignment" {
		if t.pendingTeachers == nil {
			t.pendingTeachers = map[string]bool{}
		}
		if t.pendingTeacherSchools == nil {
			t.pendingTeacherSchools = map[string]bool{}
		}
		t.pendingTeacherSchools[o.SchoolID] = true
		if o.Kind == "teacher" {
			t.pendingTeachers[o.ID] = true
		} else {
			t.pendingTeachers[o.TeacherID] = true
			if old, ok := t.objects[o.ID]; ok && old.Object.TeacherID != "" {
				t.pendingTeachers[old.Object.TeacherID] = true
			}
		}
		if o.UpdatedAt.After(t.teachingTime) {
			t.teachingTime = o.UpdatedAt
		}
	}
	if e := t.save(`INSERT INTO catalog.history(id,revision,data) VALUES($1,$2,$3)`, r, o.ID, o.Revision); e != nil {
		return e
	}
	if e := t.save(`INSERT INTO catalog.objects(id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(id) DO UPDATE SET name=excluded.name,department_id=excluded.department_id,category_id=excluded.category_id,nature_id=excluded.nature_id,course_id=excluded.course_id,term_id=excluded.term_id,term_course_id=excluded.term_course_id,class_id=excluded.class_id,teacher_id=excluded.teacher_id,data=excluded.data`, r, o.ID, o.SchoolID, o.Kind, o.Name, o.DepartmentID, o.CategoryID, o.NatureID, o.CourseID, o.TermID, o.TermCourseID, o.ClassID, o.TeacherID); e != nil {
		return e
	}
	teacherHistory := t.repoTeacherDirectory && o.Kind == "teacher"
	if !teacherHistory {
		if err := t.saveDiscoveryHistory(o); err != nil {
			return err
		}
	}
	projManaged, e := t.projectionManaged(o.SchoolID)
	if e != nil {
		return e
	}
	if !projManaged && !teacherHistory {
		if err := t.syncActivityVisibility(o); err != nil {
			return err
		}
	}
	if len(t.objects) < 10000 {
		if t.objects == nil {
			t.objects = map[string]catalog.Record{}
		}
		t.objects[o.ID] = r
	} else {
		delete(t.objects, o.ID)
	}
	return nil
}

func (t *transaction) AdvanceSourceRevision(id string, revision int64) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.objects SET data=jsonb_set(data,'{source_revision}',to_jsonb($2::bigint)) WHERE id=$1`, id, revision)
	if err != nil {
		return safe(err)
	}
	if record, ok := t.objects[id]; ok {
		record.SourceRevision = revision
		t.objects[id] = record
	}
	return nil
}

func (t *transaction) Enrollment(id string) (catalog.Enrollment, error) {
	return read[catalog.Enrollment](t, `SELECT data FROM catalog.enrollments WHERE id=$1`, id)
}

func (t *transaction) ActiveSourceEnrollments(subject, school, source string, limit int) ([]catalog.Enrollment, error) {
	return list[catalog.Enrollment](t, `SELECT data FROM catalog.enrollments WHERE subject=$1 AND school_id=$2 AND data->>'source_id'=$3 AND active ORDER BY id LIMIT $4`, subject, school, source, limit)
}
func (t *transaction) SaveEnrollment(v catalog.Enrollment) error {
	return t.save(`INSERT INTO catalog.enrollments(id,subject,school_id,course_id,term_id,active,data) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET course_id=excluded.course_id,term_id=excluded.term_id,active=excluded.active,data=excluded.data`, v, v.ID, v.Subject, v.SchoolID, v.CourseID, v.TermID, v.Active)
}
func (t *transaction) Enrollments(subject, school, course, term, after string, limit int) ([]catalog.Enrollment, error) {
	// Resolve the cursor only inside the same owner's active filtered set. Unknown
	// source terms sort last; IDs remain valid opaque cursors for old clients.
	return list[catalog.Enrollment](t, `WITH owned AS MATERIALIZED (
 SELECT e.*,COALESCE(tr.sort_key,0) AS term_order_key
 FROM catalog.enrollments e LEFT JOIN catalog.term_orders tr ON tr.id=catalog.canonical(e.term_id) AND tr.school_id=e.school_id
 WHERE e.subject=$1 AND e.active AND ($2='' OR e.school_id=$2)
 AND ($3='' OR catalog.canonical(e.course_id)=catalog.canonical($3)) AND ($4='' OR e.term_id=$4)
 ) SELECT data || jsonb_build_object('term_order_key',term_order_key,'course_id',catalog.canonical(course_id),
 'teacher_ids',(SELECT COALESCE(jsonb_agg(DISTINCT catalog.canonical(value)),'[]'::jsonb)
 FROM jsonb_array_elements_text(COALESCE(NULLIF(data->'teacher_ids','null'::jsonb),'[]'::jsonb))))
 FROM owned WHERE $5='' OR (term_order_key<(SELECT term_order_key FROM owned WHERE id=$5)
 OR (term_order_key=(SELECT term_order_key FROM owned WHERE id=$5) AND id>$5))
 ORDER BY term_order_key DESC,id LIMIT $6`, subject, school, course, term, after, limit)
}

func (t *transaction) Replay(key string) (catalog.Replay, error) {
	return read[catalog.Replay](t, `SELECT data FROM catalog.replays WHERE key=$1`, key)
}
func (t *transaction) SaveReplay(key string, v catalog.Replay) error {
	var expiresAt *time.Time
	if v.Expires && !v.Until.IsZero() {
		utc := v.Until.UTC()
		expiresAt = &utc
	}
	return t.save(`INSERT INTO catalog.replays(key,expires_at,data) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET expires_at=excluded.expires_at,data=excluded.data`, v, key, expiresAt)
}
func (r *Repository) PruneReplays(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, catalog.ErrInput
	}
	result, err := r.DB.ExecContext(ctx, `DELETE FROM catalog.replays WHERE key IN (SELECT key FROM catalog.replays WHERE expires_at IS NOT NULL AND expires_at<$1 ORDER BY expires_at,key LIMIT $2 FOR UPDATE SKIP LOCKED)`, before.UTC(), limit)
	if err != nil {
		return 0, safe(err)
	}
	n, err := result.RowsAffected()
	return n, safe(err)
}
func (t *transaction) recordAudit(v catalog.Event) error {
	result := v.Result
	if result == "" {
		result = "unknown"
	}
	reason := ""
	if v.Reason == "catalog.merge" || v.Reason == "catalog.state_change" || catalog.ValidTeacherHomepageAuditReason(v.Type, v.Reason) {
		reason = v.Reason
	}
	actor, target, action, school := v.Actor, v.Subject, v.Type, v.SchoolID
	if v.Type == "catalog.corrected.v1" {
		target = strings.TrimPrefix(target, "object/")
	}
	if actor == "" {
		actor = "unknown"
	}
	if target == "" {
		target = "unknown"
	}
	if action == "" {
		action = "unknown"
	}
	if school == "" {
		school = "*"
	}
	if e := t.save(`INSERT INTO catalog.audit(id,reason,result,audit_actor,audit_target,audit_action,audit_school,audit_time,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v, v.ID, reason, result, actor, target, action, school, v.Time); e != nil {
		return e
	}
	return nil
}

func (t *transaction) Event(v catalog.Event) error {
	if err := t.recordAudit(v); err != nil {
		return err
	}
	return t.saveEnvelope(catalog.Envelope{SpecVersion: "1.0", ID: v.ID, Source: "catalog", Type: v.Type, Subject: v.Subject, Time: v.Time, SchoolID: v.SchoolID, DataContentType: "application/json", Data: catalog.ResultData{Revision: v.Revision}})
}

func (t *transaction) HasAssignment(class, teacher string) (bool, error) {
	var yes bool
	e := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE kind='assignment' AND class_id IN (SELECT catalog.canonical($1) UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=catalog.canonical($1)) AND teacher_id IN (SELECT catalog.canonical($2) UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=catalog.canonical($2)))`, class, teacher).Scan(&yes)
	return yes, safe(e)
}
