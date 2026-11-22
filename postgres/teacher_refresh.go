package postgres

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) BeginTeacherRefreshSnapshot() error {
	_, err := t.tx.ExecContext(t.ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`)
	return safe(err)
}

// Keep body-image quota evidence in maintenance storage. Existing Admin
// clients validate the exact public refresh response shape.
type storedTeacherRefresh struct {
	c.TeacherRefreshRun
	BodyImageQuotaExceeded *bool `json:"body_image_quota_exceeded,omitempty"`
}

func (v storedTeacherRefresh) run() c.TeacherRefreshRun {
	if v.BodyImageQuotaExceeded != nil {
		v.TeacherRefreshRun.BodyImageQuotaKnown = true
		v.TeacherRefreshRun.BodyImageQuotaExceeded = *v.BodyImageQuotaExceeded
	}
	return v.TeacherRefreshRun
}

func (t *transaction) TeacherRefresh(id string) (c.TeacherRefreshRun, error) {
	v, err := read[storedTeacherRefresh](t, `SELECT data FROM catalog.teacher_refresh_runs WHERE id=$1
 UNION ALL SELECT data FROM catalog.replays WHERE key='teacher-refresh-result:' || $1`, id)
	return v.run(), err
}
func (t *transaction) TeacherRefreshLatest(school string) (*c.TeacherRefreshRun, error) {
	v, e := read[storedTeacherRefresh](t, `SELECT data FROM catalog.teacher_refresh_runs WHERE school_id=$1 ORDER BY position DESC LIMIT 1`, school)
	if errors.Is(e, c.ErrNotFound) {
		return nil, nil
	}
	run := v.run()
	return &run, e
}
func (t *transaction) LastTeacherRefreshBodyImageQuota(school string) (*c.TeacherRefreshRun, error) {
	v, err := read[storedTeacherRefresh](t, `SELECT data FROM catalog.teacher_refresh_runs
 WHERE school_id=$1 AND status='succeeded' AND data ? 'body_image_quota_exceeded' ORDER BY position DESC LIMIT 1`, school)
	if errors.Is(err, c.ErrNotFound) {
		return nil, nil
	}
	run := v.run()
	return &run, err
}
func (t *transaction) ActiveTeacherRefresh() (*c.TeacherRefreshRun, error) {
	v, e := read[storedTeacherRefresh](t, `SELECT data FROM catalog.teacher_refresh_runs WHERE status IN ('queued','running') LIMIT 1`)
	if errors.Is(e, c.ErrNotFound) {
		return nil, nil
	}
	run := v.run()
	return &run, e
}
func (t *transaction) SaveTeacherRefresh(v c.TeacherRefreshRun) error {
	// Refuse unbounded task history growth. Existing results and audit records are
	// retained; capacity maintenance is explicit rather than deleting history.
	var full bool
	if e := t.tx.QueryRowContext(t.ctx, `SELECT NOT EXISTS(SELECT 1 FROM catalog.teacher_refresh_runs WHERE id=$1) AND EXISTS(SELECT 1 FROM catalog.teacher_refresh_runs OFFSET 9999)`, v.ID).Scan(&full); e != nil {
		return safe(e)
	}
	if full {
		return c.ErrUnavailable
	}
	stored := storedTeacherRefresh{TeacherRefreshRun: v}
	if v.BodyImageQuotaKnown || v.BodyImageQuotaExceeded {
		stored.BodyImageQuotaExceeded = &v.BodyImageQuotaExceeded
	}
	return t.save(`INSERT INTO catalog.teacher_refresh_runs(id,school_id,status,requested_at,data) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data`,
		stored, v.ID, v.SchoolID, v.Status, v.RequestedAt)
}
func (t *transaction) TeacherMarkCounts(school string) (c.TeacherDirectoryResult, error) {
	return t.directoryCounts(school)
}

func (t *transaction) ReplaceTeacherUnmatched(v c.TeacherUnmatchedSnapshot) error {
	data, e := json.Marshal(v.Items)
	if e != nil {
		return c.ErrInput
	}
	var revision int64
	var same bool
	e = t.tx.QueryRowContext(t.ctx, `SELECT revision,digest=md5($2::jsonb::text) FROM catalog.teacher_unmatched_snapshots WHERE school_id=$1`, v.SchoolID, string(data)).Scan(&revision, &same)
	if e == nil {
		if revision > v.Revision || revision == v.Revision && !same {
			return c.ErrConflict
		}
		if revision == v.Revision {
			return nil
		}
	} else if !errors.Is(safe(e), c.ErrNotFound) {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_unmatched_sites WHERE school_id=$1`, v.SchoolID); e != nil {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_unmatched_sites(school_id,site_id,reason,data)
 SELECT $1,item->>'site_id',item->>'reason',item FROM jsonb_array_elements($2::jsonb) item`, v.SchoolID, string(data)); e != nil {
		return safe(e)
	}
	_, e = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_unmatched_snapshots(school_id,revision,observed_at,digest) VALUES($1,$2,$3,md5($4::jsonb::text))
 ON CONFLICT(school_id) DO UPDATE SET revision=excluded.revision,observed_at=excluded.observed_at,digest=excluded.digest`, v.SchoolID, v.Revision, v.ObservedAt, string(data))
	return safe(e)
}
func (t *transaction) TeacherUnmatched(school, reason, after string, limit int) (c.UnmatchedTeacherSitePage, error) {
	return t.SearchTeacherUnmatched(school, reason, "", after, limit)
}

func (t *transaction) SearchTeacherUnmatched(school, reason, query, after string, limit int) (c.UnmatchedTeacherSitePage, error) {
	out := c.UnmatchedTeacherSitePage{Items: []c.UnmatchedTeacherSite{}, ReasonCounts: map[string]int64{}}
	e := t.tx.QueryRowContext(t.ctx, `SELECT revision,observed_at FROM catalog.teacher_unmatched_snapshots WHERE school_id=$1`, school).Scan(&out.Revision, &out.ObservedAt)
	if errors.Is(safe(e), c.ErrNotFound) {
		return out, nil
	}
	if e != nil {
		return out, safe(e)
	}
	var raw []byte
	e = t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(jsonb_object_agg(reason,n),'{}'::jsonb) FROM (SELECT reason,count(*) n FROM catalog.teacher_unmatched_sites WHERE school_id=$1 GROUP BY reason) x`, school).Scan(&raw)
	if e != nil {
		return out, safe(e)
	}
	if json.Unmarshal(raw, &out.ReasonCounts) != nil {
		return out, c.ErrUnavailable
	}
	if query == "" {
		for key, count := range out.ReasonCounts {
			if reason == "" || reason == key {
				out.Total += count
			}
		}
		out.Items, e = list[c.UnmatchedTeacherSite](t, `SELECT data FROM catalog.teacher_unmatched_sites WHERE school_id=$1 AND ($2='' OR reason=$2) AND site_id>$3 ORDER BY site_id LIMIT $4`, school, reason, after, limit)
		return out, e
	}
	const literalFilter = `AND (strpos(lower(COALESCE(data->>'name','')),lower($3))>0 OR strpos(lower(COALESCE(data->>'unit','')),lower($3))>0)`
	if e = t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM catalog.teacher_unmatched_sites WHERE school_id=$1 AND ($2='' OR reason=$2) `+literalFilter, school, reason, query).Scan(&out.Total); e != nil {
		return out, safe(e)
	}
	out.Items, e = list[c.UnmatchedTeacherSite](t, `SELECT data FROM catalog.teacher_unmatched_sites WHERE school_id=$1 AND ($2='' OR reason=$2) `+literalFilter+` AND site_id>$4 ORDER BY site_id LIMIT $5`, school, reason, query, after, limit)
	return out, e
}

func (t *transaction) RecordTeacherRefreshAudit(v c.Event) error { return t.recordAudit(v) }

func (r *Repository) PruneTeacherRefreshRuns(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, c.ErrInput
	}
	result, err := r.DB.ExecContext(ctx, `WITH removed AS (DELETE FROM catalog.teacher_refresh_runs WHERE id IN (
 SELECT r.id FROM catalog.teacher_refresh_runs r
 WHERE r.status IN ('succeeded','failed','timed_out') AND r.requested_at<$1
   AND r.id NOT IN (SELECT DISTINCT ON (school_id) id FROM catalog.teacher_refresh_runs ORDER BY school_id,position DESC)
 ORDER BY r.requested_at,r.id LIMIT $2 FOR UPDATE OF r SKIP LOCKED
) RETURNING id,data)
 INSERT INTO catalog.replays(key,data) SELECT 'teacher-refresh-result:' || id,data FROM removed`, before.UTC(), limit)
	if err != nil {
		return 0, safe(err)
	}
	n, err := result.RowsAffected()
	return n, safe(err)
}
