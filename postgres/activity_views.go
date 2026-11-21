package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	m "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) RecordView(actor string, o m.Object, at time.Time) (bool, error) {
	if actor == "" || len(actor) > 2048 || at.IsZero() {
		return false, m.ErrInput
	}
	var canonical string
	if t.projectionRead {
		if e := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE((SELECT a.canonical_id FROM catalog.projection_heads h JOIN catalog.projection_aliases a ON a.version=h.version WHERE h.school_id=$1 AND a.alias_id=$2), catalog.canonical($2))`, o.SchoolID, o.ID).Scan(&canonical); e != nil {
			return false, safe(e)
		}
	} else {
		if e := t.tx.QueryRowContext(t.ctx, `SELECT catalog.canonical($1)`, o.ID).Scan(&canonical); e != nil {
			return false, safe(e)
		}
	}
	o.ID = canonical
	var locked string
	// Mutations lock source rows before publishing a new projection head.
	// Synthetic directory objects have only the head to lock.
	lockErr := t.tx.QueryRowContext(t.ctx, `SELECT id FROM catalog.objects WHERE id=$1 FOR SHARE`, o.ID).Scan(&locked)
	if lockErr != nil && !errors.Is(lockErr, sql.ErrNoRows) {
		return false, safe(lockErr)
	}
	if t.projectionRead {
		if err := t.tx.QueryRowContext(t.ctx, `SELECT school_id FROM catalog.projection_heads WHERE school_id=$1 FOR SHARE`, o.SchoolID).Scan(&locked); err != nil {
			return false, safe(err)
		}
	} else if errors.Is(lockErr, sql.ErrNoRows) {
		if err := t.tx.QueryRowContext(t.ctx, `SELECT h.school_id FROM catalog.projection_heads h JOIN catalog.projection_objects p ON p.version=h.version WHERE h.school_id=$1 AND p.id=$2 AND p.visible FOR SHARE OF h`, o.SchoolID, o.ID).Scan(&locked); err != nil {
			return false, safe(err)
		}
	}
	current, e := t.PublicObject(o.ID)
	if !t.projectionRead && errors.Is(e, m.ErrNotFound) {
		current, e = t.projectedObject(o.ID)
	}
	if e != nil {
		return false, e
	}
	if current.Inactive || current.CanonicalID != "" || current.ID != o.ID || current.Kind != o.Kind || current.SchoolID != o.SchoolID || (o.Kind != "course" && o.Kind != "teacher") {
		return false, m.ErrForbidden
	}
	day := m.ActivityDate(at)
	date := day.Format(time.DateOnly)
	var before time.Time
	if _, e = t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('catalog:view-retention',0))`); e != nil {
		return false, safe(e)
	}
	if e = t.tx.QueryRowContext(t.ctx, `SELECT before_day FROM catalog.view_retention WHERE singleton`).Scan(&before); e != nil {
		return false, safe(e)
	}
	if day.Before(m.ActivityDate(before)) {
		return false, m.ErrInput
	}
	secret, e := t.viewDayKey(date)
	if e != nil {
		return false, e
	}
	defer clear(secret)
	// UTC tokens cannot determine whether the view belongs to this Beijing date.
	raw, _ := json.Marshal([]string{"catalog.view.v3", date, actor, o.Kind, o.ID})
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	key := fmt.Sprintf("%x", mac.Sum(nil))
	result, e := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.view_dedup(digest,day) VALUES($1,$2) ON CONFLICT DO NOTHING`, key, date)
	if e != nil {
		return false, safe(e)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return false, safe(e)
	}
	if n == 0 {
		return false, nil
	}
	e = t.changeActivity(o.Kind, o.ID, o.SchoolID, day, 1, 0, nil, at)
	return e == nil, e
}

// Keep only the current Beijing day's opaque tokens. Cleaning never changes counts.
func (r *Repository) PruneViewDedup(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return 0, m.ErrInput
	}
	before := m.ActivityDate(now).Format(time.DateOnly)
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return 0, safe(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('catalog:view-retention',0))`); e != nil {
		return 0, safe(e)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE catalog.view_retention SET before_day=GREATEST(before_day,$1) WHERE singleton`, before); e != nil {
		return 0, safe(e)
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM catalog.view_dedup WHERE (digest,day) IN (SELECT digest,day FROM catalog.view_dedup WHERE day<$1 ORDER BY day,digest LIMIT $2)`, before, limit)
	if e != nil {
		return 0, safe(e)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return 0, safe(e)
	}
	return n, safe(tx.Commit())
}

func (t *transaction) viewDayKey(day string) ([]byte, error) {
	var key []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT key FROM catalog.view_day_keys WHERE day=$1`, day).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		generated := make([]byte, 32)
		if _, err := rand.Read(generated); err != nil {
			return nil, m.ErrUnavailable
		}
		defer clear(generated)
		err = t.tx.QueryRowContext(t.ctx, `INSERT INTO catalog.view_day_keys(day,key) VALUES($1,$2)
 ON CONFLICT(day) DO UPDATE SET key=catalog.view_day_keys.key RETURNING key`, day, generated).Scan(&key)
	}
	if err != nil {
		return nil, safe(err)
	}
	if len(key) != 32 {
		clear(key)
		return nil, m.ErrUnavailable
	}
	return key, nil
}

// Day keys cannot identify a user or object. Removing an old key also prevents
// linkage of any expired tokens still awaiting the bounded maintenance sweep.
func (r *Repository) PruneViewKeys(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return 0, m.ErrInput
	}
	result, err := r.DB.ExecContext(ctx, `DELETE FROM catalog.view_day_keys WHERE day IN (SELECT day FROM catalog.view_day_keys WHERE day<$1 ORDER BY day LIMIT $2)`, m.ActivityDate(now).Format(time.DateOnly), limit)
	if err != nil {
		return 0, safe(err)
	}
	n, err := result.RowsAffected()
	return n, safe(err)
}
func (t *transaction) syncActivityVisibility(o m.Object) error {
	if o.Kind != "course" && o.Kind != "teacher" {
		return nil
	}
	var canonical string
	if e := t.tx.QueryRowContext(t.ctx, `SELECT catalog.canonical($1)`, o.ID).Scan(&canonical); e != nil {
		return safe(e)
	}
	visible := !o.Inactive && canonical == o.ID
	// Work in bounded batches; row locks and business writes share this transaction.
	after := ""
	for {
		rows, e := t.tx.QueryContext(t.ctx, `SELECT id,day FROM catalog.activity_buckets WHERE kind=$1 AND target_id=$2 AND visible<>($3 AND first_count>0) AND id COLLATE "C">$4 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100`, o.Kind, o.ID, visible, after)
		if e != nil {
			return safe(e)
		}
		type item struct {
			id  string
			day time.Time
		}
		items := []item{}
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.day); e != nil {
				rows.Close()
				return safe(e)
			}
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return safe(e)
		}
		if len(items) == 0 {
			return nil
		}
		for _, v := range items {
			if e = t.changeActivity(o.Kind, o.ID, o.SchoolID, v.day, 0, 0, &visible, o.UpdatedAt); e != nil {
				return e
			}
			after = v.id
		}
	}
}

func (t *transaction) syncProjectedActivityVisibility(school string, version int64, at time.Time) error {
	after := ""
	for {
		rows, err := t.tx.QueryContext(t.ctx, `SELECT b.id, b.kind, b.target_id, b.day,
 EXISTS(SELECT 1 FROM catalog.projection_objects o WHERE o.version=$2 AND o.id=b.target_id AND o.kind=b.kind AND o.visible) AS target_visible
 FROM catalog.activity_buckets b
 WHERE b.school_id=$1
   AND b.visible <> (EXISTS(SELECT 1 FROM catalog.projection_objects o WHERE o.version=$2 AND o.id=b.target_id AND o.kind=b.kind AND o.visible) AND (b.first_count>0 OR b.second_count>0))
   AND b.id COLLATE "C">$3 COLLATE "C"
 ORDER BY b.id COLLATE "C" LIMIT 100`, school, version, after)
		if err != nil {
			return safe(err)
		}
		type item struct {
			id      string
			kind    string
			target  string
			day     time.Time
			visible bool
		}
		items := []item{}
		for rows.Next() {
			var v item
			if err = rows.Scan(&v.id, &v.kind, &v.target, &v.day, &v.visible); err != nil {
				rows.Close()
				return safe(err)
			}
			items = append(items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return safe(err)
		}
		if len(items) == 0 {
			return nil
		}
		for _, v := range items {
			vis := v.visible
			if err = t.changeActivity(v.kind, v.target, school, v.day, 0, 0, &vis, at); err != nil {
				return err
			}
			after = v.id
		}
	}
}

func (t *transaction) syncTeacherActivityVisibility(school string, ids []string, at time.Time) error {
	after := ""
	expectedExpr := `EXISTS(SELECT 1 FROM catalog.objects o WHERE o.id=b.target_id AND o.school_id=$1 AND o.kind='teacher' AND o.id=catalog.canonical(o.id) AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND ` + markedTeacherSQL("o") + `)`
	for {
		var rows *sql.Rows
		var err error
		if ids == nil {
			rows, err = t.tx.QueryContext(t.ctx, `SELECT b.id, b.target_id, b.day, (`+expectedExpr+`) AS target_visible
 FROM catalog.activity_buckets b
 WHERE b.school_id=$1 AND b.kind='teacher'
   AND b.visible <> ((`+expectedExpr+`) AND (b.first_count>0 OR b.second_count>0))
   AND b.id COLLATE "C">$2 COLLATE "C"
 ORDER BY b.id COLLATE "C" LIMIT 100`, school, after)
		} else {
			rows, err = t.tx.QueryContext(t.ctx, `SELECT b.id, b.target_id, b.day, (`+expectedExpr+`) AS target_visible
 FROM catalog.activity_buckets b
 WHERE b.school_id=$1 AND b.kind='teacher'
   AND b.target_id IN (SELECT catalog.canonical(id) FROM unnest($3::text[]) u(id) UNION SELECT id FROM unnest($3::text[]) u(id))
   AND b.visible <> ((`+expectedExpr+`) AND (b.first_count>0 OR b.second_count>0))
   AND b.id COLLATE "C">$2 COLLATE "C"
 ORDER BY b.id COLLATE "C" LIMIT 100`, school, after, ids)
		}
		if err != nil {
			return safe(err)
		}
		type item struct {
			id      string
			target  string
			day     time.Time
			visible bool
		}
		items := []item{}
		for rows.Next() {
			var v item
			if err = rows.Scan(&v.id, &v.target, &v.day, &v.visible); err != nil {
				rows.Close()
				return safe(err)
			}
			items = append(items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return safe(err)
		}
		if len(items) == 0 {
			return nil
		}
		for _, v := range items {
			vis := v.visible
			if err = t.changeActivity("teacher", v.target, school, v.day, 0, 0, &vis, at); err != nil {
				return err
			}
			after = v.id
		}
	}
}
