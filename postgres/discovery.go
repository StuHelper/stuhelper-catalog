package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
	"time"
)

var _ c.DiscoverySource = (*Repository)(nil)

// Source history supports unprojected maintenance reads. Only a directory
// publication may enqueue discovery documents for search.
func (t *transaction) saveDiscoveryHistory(o c.Object) error {
	if o.Kind != "school" && o.Kind != "course" && o.Kind != "teacher" {
		return nil
	}
	var canonical string
	if e := t.tx.QueryRowContext(t.ctx, `SELECT catalog.canonical($1)`, o.ID).Scan(&canonical); e != nil {
		return safe(e)
	}
	d := c.DiscoveryDocument{Target: c.DiscoveryTarget{Source: "catalog", ID: o.ID, Kind: o.Kind}, Revision: o.Revision, Title: o.Name, SchoolID: o.SchoolID, Visible: !o.Inactive && canonical == o.ID, At: o.UpdatedAt}
	if (o.Kind == "school" || o.Kind == "course") && d.Visible {
		d.Code = o.Code
		d.Aliases = append([]string{}, o.Aliases...)
	}
	if o.Kind == "course" && d.Visible {
		d.EnglishName = o.EnglishName
		d.FormerCodes = append([]string{}, o.FormerCodes...)
		d.FormerNames = append([]string{}, o.FormerNames...)
	}
	if !d.Visible {
		d.Title = ""
	}
	return t.save(`INSERT INTO catalog.discovery_history(id,revision,data) VALUES($1,$2,$3)`, d, o.ID, o.Revision)
}
func (t *transaction) DiscoveryCurrent(school string, ids []string) ([]c.DiscoveryDocument, error) {
	if t.projectionRead {
		if _, err := t.projectionVersion(school); err != nil {
			return nil, err
		}
		return list[c.DiscoveryDocument](t, `SELECT d.data FROM catalog.projection_heads h JOIN catalog.projection_documents d ON d.version=h.version
 WHERE h.school_id=$1 AND d.id=ANY($2::text[]) AND (d.data->>'visible')::boolean ORDER BY d.id COLLATE "C"`, school, ids)
	}
	return list[c.DiscoveryDocument](t, `SELECT h.data FROM catalog.objects o CROSS JOIN LATERAL (SELECT data FROM catalog.discovery_history h WHERE h.id=o.id ORDER BY h.revision DESC LIMIT 1) h WHERE o.id=ANY($1::text[]) AND o.school_id=$2 AND o.id=catalog.canonical(o.id) AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND (h.data->>'visible')::boolean `+t.teacherVisibility("o")+` ORDER BY o.id COLLATE "C"`, ids, school)
}

func (t *transaction) syncTeacherDiscoveryAndActivity(school string, ids []string, at time.Time) error {
	if !t.repoTeacherDirectory || school == "" {
		return nil
	}
	projManaged, err := t.projectionManaged(school)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	filter := ""
	args := []any{school}
	if ids != nil {
		filter = ` AND o.id IN (SELECT catalog.canonical(id) FROM unnest($2::text[]) u(id) UNION SELECT id FROM unnest($2::text[]) u(id))`
		args = append(args, ids)
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT o.data->'object', catalog.canonical(o.id), (`+markedTeacherSQL("o")+`), COALESCE(h.revision, 0), h.data
 FROM catalog.objects o
 LEFT JOIN LATERAL (SELECT revision, data FROM catalog.discovery_history h WHERE h.id=o.id ORDER BY h.revision DESC LIMIT 1) h ON true
 WHERE o.school_id=$1 AND o.kind='teacher'`+filter+` ORDER BY o.id COLLATE "C"`, args...)
	if err != nil {
		return safe(err)
	}
	type candidate struct {
		obj          c.Object
		canonical    string
		marked       bool
		lastRevision int64
		prev         *c.DiscoveryDocument
	}
	items := []candidate{}
	for rows.Next() {
		var rawObj []byte
		var rawPrev []byte
		var item candidate
		if err := rows.Scan(&rawObj, &item.canonical, &item.marked, &item.lastRevision, &rawPrev); err != nil {
			rows.Close()
			return safe(err)
		}
		if json.Unmarshal(rawObj, &item.obj) != nil {
			rows.Close()
			return c.ErrUnavailable
		}
		if len(rawPrev) > 0 {
			var prev c.DiscoveryDocument
			if json.Unmarshal(rawPrev, &prev) != nil {
				rows.Close()
				return c.ErrUnavailable
			}
			item.prev = &prev
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return safe(err)
	}
	rows.Close()

	for _, item := range items {
		o := item.obj
		ts := at
		if !o.UpdatedAt.IsZero() && o.UpdatedAt.After(ts) {
			ts = o.UpdatedAt
		}
		visible := !o.Inactive && item.canonical == o.ID && item.marked
		title := o.Name
		if !visible {
			title = ""
		}
		needEmit := item.prev == nil || o.Revision > item.lastRevision || item.prev.Visible != visible || item.prev.Title != title || item.prev.SchoolID != o.SchoolID
		if !needEmit {
			continue
		}
		rev := o.Revision
		if item.lastRevision+1 > rev {
			rev = item.lastRevision + 1
		}
		d := c.DiscoveryDocument{
			Target:   c.DiscoveryTarget{Source: "catalog", ID: o.ID, Kind: "teacher"},
			Revision: rev,
			Title:    title,
			SchoolID: o.SchoolID,
			Visible:  visible,
			At:       ts,
		}
		if err := t.save(`INSERT INTO catalog.discovery_history(id,revision,data) VALUES($1,$2,$3)`, d, o.ID, rev); err != nil {
			return err
		}

	}
	if projManaged {
		return nil
	}
	return t.syncTeacherActivityVisibility(school, ids, at)
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
	if r.RequireDirectoryProjection {
		return r.projectedDiscoverySnapshot(ctx, cursor, limit)
	}
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
