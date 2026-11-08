package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
	"time"
)

var _ c.DiscoverySource = (*Repository)(nil)

func (t *transaction) saveDiscovery(o c.Object) error {
	if o.Kind != "course" && o.Kind != "teacher" {
		return nil
	}
	var canonical string
	if e := t.tx.QueryRowContext(t.ctx, `SELECT catalog.canonical($1)`, o.ID).Scan(&canonical); e != nil {
		return safe(e)
	}
	d := c.DiscoveryDocument{Target: c.DiscoveryTarget{Source: "catalog", ID: o.ID, Kind: o.Kind}, Revision: o.Revision, Title: o.Name, SchoolID: o.SchoolID, Visible: !o.Inactive && canonical == o.ID, At: o.UpdatedAt}
	if !d.Visible {
		d.Title = ""
	}
	if e := t.save(`INSERT INTO catalog.discovery_history(id,revision,data) VALUES($1,$2,$3)`, d, o.ID, o.Revision); e != nil {
		return e
	}
	eventID := fmt.Sprintf("discovery:%s:%d", o.ID, o.Revision)
	return t.saveEnvelope(c.Envelope{SpecVersion: "1.0", ID: eventID, Source: "catalog", Type: "catalog.discovery.v1", Subject: o.ID, Time: o.UpdatedAt, Data: c.ResultData{Revision: d.Revision, Target: &d.Target, Title: &d.Title, SchoolID: &d.SchoolID, Visible: &d.Visible, Ratings: &d.Ratings, At: &d.At}})
}
func (t *transaction) DiscoveryCurrent(school string, ids []string) ([]c.DiscoveryDocument, error) {
	return list[c.DiscoveryDocument](t, `SELECT h.data FROM catalog.objects o JOIN catalog.discovery_history h ON h.id=o.id AND h.revision=(o.data->'object'->>'revision')::bigint WHERE o.id=ANY($1::text[]) AND o.school_id=$2 AND o.id=catalog.canonical(o.id) AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND (h.data->>'visible')::boolean ORDER BY o.id COLLATE "C"`, ids, school)
}

type discoveryCursor struct {
	Visibility string
	Epoch      int64
	After      string
	Expires    time.Time
}

// DiscoverySnapshot preserves object versions using transaction visibility on
// immutable history. Retention invalidates existing cursors, never silently resets.
func (r *Repository) DiscoverySnapshot(ctx context.Context, cursor string, limit int) (out c.DiscoveryPage, err error) {
	out.Items = []c.DiscoveryDocument{}
	if limit < 1 || limit > 100 || len(cursor) > 16384 {
		return out, c.ErrInput
	}
	state := discoveryCursor{}
	if cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(b, &state) != nil || !c.ValidID(state.After) || state.Visibility == "" || !time.Now().Before(state.Expires) {
			return out, c.ErrInput
		}
	}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return out, safe(e)
	}
	defer tx.Rollback()
	var epoch int64
	if e = tx.QueryRowContext(ctx, `SELECT revision FROM catalog.discovery_retention WHERE singleton`).Scan(&epoch); e != nil {
		return out, safe(e)
	}
	if cursor == "" {
		state.Epoch = epoch
		state.Expires = time.Now().Add(5 * time.Minute)
		if e = tx.QueryRowContext(ctx, `SELECT pg_current_snapshot()::text`).Scan(&state.Visibility); e != nil {
			return out, safe(e)
		}
	} else if epoch != state.Epoch {
		return out, c.ErrUnavailable
	}
	if len(state.Visibility) > 8192 || strings.ContainsAny(state.Visibility, "\n\r") {
		return out, c.ErrUnavailable
	}
	t := &transaction{tx: tx, ctx: ctx}
	rows, e := list[c.DiscoveryDocument](t, `SELECT data FROM (SELECT DISTINCT ON (id COLLATE "C") id,data FROM catalog.discovery_history WHERE id COLLATE "C">$2 COLLATE "C" AND pg_visible_in_snapshot(recorded_xid,$1::pg_snapshot) ORDER BY id COLLATE "C",revision DESC) latest ORDER BY id COLLATE "C" LIMIT $3`, state.Visibility, state.After, limit+1)
	if e != nil {
		return out, e
	}
	if len(rows) > limit {
		rows = rows[:limit]
		state.After = rows[len(rows)-1].Target.ID
		b, e := json.Marshal(state)
		if e != nil {
			return out, c.ErrUnavailable
		}
		out.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	out.Items = rows
	return out, safe(tx.Commit())
}
