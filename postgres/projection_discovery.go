package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

type projectionDiscoveryCursor struct {
	Versions []int64   `json:"versions"`
	After    string    `json:"after"`
	Expires  time.Time `json:"expires"`
}

// Search consumes this public catalog port. A cursor keeps one complete set of
// immutable generations; pruning invalidates it explicitly instead of mixing data.
func (r *Repository) projectedDiscoverySnapshot(ctx context.Context, cursor string, limit int) (out c.DiscoveryPage, err error) {
	out.Items = []c.DiscoveryDocument{}
	if limit < 1 || limit > 100 || len(cursor) > 16384 {
		return out, c.ErrInput
	}
	state := projectionDiscoveryCursor{Expires: time.Now().Add(5 * time.Minute)}
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(raw, &state) != nil || !c.ValidID(state.After) || len(state.Versions) < 1 || len(state.Versions) > 1000 || !time.Now().Before(state.Expires) {
			return out, c.ErrInput
		}
	}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return out, safe(e)
	}
	defer tx.Rollback()
	if cursor == "" {
		rows, e := tx.QueryContext(ctx, `SELECT version FROM catalog.projection_heads ORDER BY school_id LIMIT 1001`)
		if e != nil {
			return out, safe(e)
		}
		for rows.Next() {
			var version int64
			if e = rows.Scan(&version); e != nil {
				rows.Close()
				return out, safe(e)
			}
			state.Versions = append(state.Versions, version)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, safe(e)
		}
		if len(state.Versions) < 1 || len(state.Versions) > 1000 {
			return out, c.ErrUnavailable
		}
	} else {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM catalog.projection_generations WHERE version=ANY($1::bigint[])`, state.Versions).Scan(&count); e != nil {
			return out, safe(e)
		}
		if count != len(state.Versions) {
			return out, c.ErrUnavailable
		}
	}
	t := &transaction{ctx: ctx, tx: tx}
	out.Items, e = list[c.DiscoveryDocument](t, `SELECT data FROM catalog.projection_documents WHERE version=ANY($1::bigint[]) AND id COLLATE "C">$2 COLLATE "C" ORDER BY id COLLATE "C" LIMIT $3`, state.Versions, state.After, limit+1)
	if e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		state.After = out.Items[len(out.Items)-1].Target.ID
		raw, e := json.Marshal(state)
		if e != nil {
			return out, c.ErrUnavailable
		}
		out.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, safe(tx.Commit())
}
