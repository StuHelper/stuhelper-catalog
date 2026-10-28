// Package postgres is the sole owner of catalog SQL and migrations.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"

	catalog "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var Schema string

type Repository struct{ DB *sql.DB }
type transaction struct {
	tx  *sql.Tx
	ctx context.Context
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
	if errors.As(e, &pg) && (pg.Code == "23505" || pg.Code == "40001" || pg.Code == "40P01") {
		return catalog.ErrConflict
	}
	return catalog.ErrUnavailable
}
func (r *Repository) Transaction(ctx context.Context, run func(catalog.Transaction) error) error {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return safe(e)
	}
	defer tx.Rollback()
	if e = run(&transaction{tx, ctx}); e != nil {
		return e
	}
	return safe(tx.Commit())
}
func (t *transaction) Lock(key string) error {
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "catalog:"+key)
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
	return read[catalog.Record](t, `SELECT data FROM catalog.objects WHERE id=$1`, id)
}
func (t *transaction) SaveObject(r catalog.Record) error {
	o := r.Object
	if e := t.save(`INSERT INTO catalog.history(id,revision,data) VALUES($1,$2,$3)`, r, o.ID, o.Revision); e != nil {
		return e
	}
	return t.save(`INSERT INTO catalog.objects(id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(id) DO UPDATE SET name=excluded.name,department_id=excluded.department_id,category_id=excluded.category_id,nature_id=excluded.nature_id,course_id=excluded.course_id,term_id=excluded.term_id,term_course_id=excluded.term_course_id,class_id=excluded.class_id,teacher_id=excluded.teacher_id,data=excluded.data`, r, o.ID, o.SchoolID, o.Kind, o.Name, o.DepartmentID, o.CategoryID, o.NatureID, o.CourseID, o.TermID, o.TermCourseID, o.ClassID, o.TeacherID)
}

func (t *transaction) Enrollment(id string) (catalog.Enrollment, error) {
	return read[catalog.Enrollment](t, `SELECT data FROM catalog.enrollments WHERE id=$1`, id)
}
func (t *transaction) SaveEnrollment(v catalog.Enrollment) error {
	return t.save(`INSERT INTO catalog.enrollments(id,subject,school_id,course_id,term_id,active,data) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET course_id=excluded.course_id,term_id=excluded.term_id,active=excluded.active,data=excluded.data`, v, v.ID, v.Subject, v.SchoolID, v.CourseID, v.TermID, v.Active)
}
func (t *transaction) Enrollments(subject, school, course, term, after string, limit int) ([]catalog.Enrollment, error) {
	return list[catalog.Enrollment](t, `SELECT data FROM catalog.enrollments WHERE subject=$1 AND ($2='' OR school_id=$2) AND ($3='' OR course_id=$3) AND ($4='' OR term_id=$4) AND id>$5 AND active ORDER BY id LIMIT $6`, subject, school, course, term, after, limit)
}
func (t *transaction) Replay(key string) (catalog.Replay, error) {
	return read[catalog.Replay](t, `SELECT data FROM catalog.replays WHERE key=$1`, key)
}
func (t *transaction) SaveReplay(key string, v catalog.Replay) error {
	return t.save(`INSERT INTO catalog.replays(key,data) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET data=excluded.data`, v, key)
}
func (t *transaction) Event(v catalog.Event) error {
	if e := t.save(`INSERT INTO catalog.audit(id,data) VALUES($1,$2)`, v, v.ID); e != nil {
		return e
	}
	return t.save(`INSERT INTO catalog.outbox(id,data) VALUES($1,$2)`, map[string]any{"specversion": "1.0", "id": v.ID, "source": "catalog", "type": v.Type, "subject": v.Subject, "time": v.Time, "schoolid": v.SchoolID, "datacontenttype": "application/json", "data": map[string]any{"revision": v.Revision}}, v.ID)
}

func (t *transaction) HasAssignment(class, teacher string) (bool, error) {
	var yes bool
	e := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE kind='assignment' AND class_id=$1 AND teacher_id=$2)`, class, teacher).Scan(&yes)
	return yes, safe(e)
}
