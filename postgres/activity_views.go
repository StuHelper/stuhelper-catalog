package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	m "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) RecordView(actor string, o m.Object, at time.Time) (bool, error) {
	if actor == "" || len(actor) > 2048 || at.IsZero() {
		return false, m.ErrInput
	}
	var canonical string
	if e := t.tx.QueryRowContext(t.ctx, `SELECT catalog.canonical($1)`, o.ID).Scan(&canonical); e != nil {
		return false, safe(e)
	}
	o.ID = canonical
	var locked string
	if e := t.tx.QueryRowContext(t.ctx, `SELECT id FROM catalog.objects WHERE id=$1 FOR SHARE`, o.ID).Scan(&locked); e != nil {
		return false, safe(e)
	}
	current, e := t.PublicObject(o.ID)
	if e != nil {
		return false, e
	}
	if current.Inactive || current.CanonicalID != "" || current.ID != o.ID || current.Kind != o.Kind || current.SchoolID != o.SchoolID || (o.Kind != "course" && o.Kind != "teacher") {
		return false, m.ErrForbidden
	}
	day := at.UTC().Truncate(24 * time.Hour)
	var before time.Time
	if _, e = t.tx.ExecContext(t.ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('catalog:view-retention',0))`); e != nil {
		return false, safe(e)
	}
	if e = t.tx.QueryRowContext(t.ctx, `SELECT before_day FROM catalog.view_retention WHERE singleton`).Scan(&before); e != nil {
		return false, safe(e)
	}
	if day.Before(before) {
		return false, m.ErrInput
	}
	raw, _ := json.Marshal([]string{"catalog.view.v1", actor, o.Kind, o.ID})
	key := fmt.Sprintf("%x", sha256.Sum256(raw))
	result, e := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.view_dedup(digest,day) VALUES($1,$2) ON CONFLICT DO NOTHING`, key, day)
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

// Keep the current day plus 31 preceding UTC days. Cleaning never changes counts.
func (r *Repository) PruneViewDedup(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return 0, m.ErrInput
	}
	before := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -31)
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
