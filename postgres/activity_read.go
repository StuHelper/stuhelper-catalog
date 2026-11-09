package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	m "github.com/StuHelper/stuhelper-catalog"
	"time"
)

var _ m.ActivityReader = (*Repository)(nil)

type activityCursor struct {
	Version           int
	From, To          time.Time
	Limit             int
	After, Visibility string
	Epoch             int64
	Expires           time.Time
}

func (r *Repository) ReadActivity(ctx context.Context, id string) (m.Activity, error) {
	var d m.Activity
	if _, _, e := m.ParseActivityID(id); e != nil {
		return d, e
	}
	var raw []byte
	if e := r.DB.QueryRowContext(ctx, `SELECT data FROM catalog.activity_buckets WHERE id=$1`, id).Scan(&raw); e != nil {
		return d, safe(e)
	}
	if json.Unmarshal(raw, &d) != nil {
		return d, m.ErrUnavailable
	}
	return d, nil
}
func (r *Repository) ActivitySnapshot(ctx context.Context, fromDay, toDay time.Time, cursor string, limit int) ([]m.Activity, string, error) {
	out := []m.Activity{}
	if !m.ActivityDay(fromDay) || !m.ActivityDay(toDay) || !fromDay.Before(toDay) || limit < 1 || limit > 100 || len(cursor) > 16384 {
		return out, "", m.ErrInput
	}
	state := activityCursor{Version: 1, From: fromDay, To: toDay, Limit: limit, Expires: time.Now().Add(5 * time.Minute)}
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(raw, &state) != nil || state.Version != 1 || !state.From.Equal(fromDay) || !state.To.Equal(toDay) || state.Limit != limit || state.After == "" || len(state.After) > 512 || state.Visibility == "" || !time.Now().Before(state.Expires) {
			return out, "", m.ErrInput
		}
	}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return out, "", safe(e)
	}
	defer tx.Rollback()
	var epoch int64
	if e = tx.QueryRowContext(ctx, `SELECT revision FROM catalog.activity_retention WHERE singleton`).Scan(&epoch); e != nil {
		return out, "", safe(e)
	}
	if cursor == "" {
		state.Epoch = epoch
		if e = tx.QueryRowContext(ctx, `SELECT pg_current_snapshot()::text`).Scan(&state.Visibility); e != nil {
			return out, "", safe(e)
		}
	} else if state.Epoch != epoch {
		return out, "", m.ErrUnavailable
	}
	if len(state.Visibility) > 8192 {
		return out, "", m.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT data FROM (SELECT DISTINCT ON(id COLLATE "C") id,data FROM catalog.activity_history WHERE day>=$1 AND day<$2 AND id COLLATE "C">$3 COLLATE "C" AND pg_visible_in_snapshot(recorded_xid,$4::pg_snapshot) ORDER BY id COLLATE "C",revision DESC) latest ORDER BY id COLLATE "C" LIMIT $5`, fromDay, toDay, state.After, state.Visibility, limit+1)
	if e != nil {
		return out, "", safe(e)
	}
	for rows.Next() {
		var raw []byte
		var d m.Activity
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &d) != nil {
			rows.Close()
			return out, "", m.ErrUnavailable
		}
		out = append(out, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, "", safe(e)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		state.After = out[len(out)-1].ID
		raw, e := json.Marshal(state)
		if e != nil {
			return nil, "", m.ErrUnavailable
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	if e = tx.Commit(); e != nil {
		return nil, "", safe(e)
	}
	return out, next, nil
}

// PruneActivityHistory preserves the latest revision, including zero tombstones.
// Removing older versions invalidates cursors through the same-transaction epoch.
func (r *Repository) PruneActivityHistory(ctx context.Context, beforeDay time.Time, limit int) (int64, error) {
	if !m.ActivityDay(beforeDay) || limit < 1 || limit > 1000 {
		return 0, m.ErrInput
	}
	result, e := r.DB.ExecContext(ctx, `DELETE FROM catalog.activity_history WHERE (id,revision) IN (SELECT h.id,h.revision FROM catalog.activity_history h JOIN catalog.activity_buckets b USING(id) WHERE h.day<$1 AND h.revision<b.revision ORDER BY h.day,h.id,h.revision LIMIT $2)`, beforeDay, limit)
	if e != nil {
		return 0, safe(e)
	}
	n, e := result.RowsAffected()
	return n, safe(e)
}
