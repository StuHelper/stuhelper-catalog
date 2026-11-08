package catalog_test

import (
	"context"
	"database/sql"
	"encoding/json"

	a "github.com/StuHelper/stuhelper-catalog/auditquery"

	"strings"
	"testing"
	"time"
)

func TestPublicAuditQuery(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	put := func(tx interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}, id, school string, when time.Time) string {
		t.Helper()
		_, e := tx.ExecContext(ctx, `INSERT INTO catalog.audit(id,audit_actor,audit_target,audit_action,audit_school,audit_time,data) VALUES($1,'actor','target','changed',$3,$2,jsonb_build_object('secret','DO_NOT_EXPOSE'))`, id, when.Format(time.RFC3339Nano), school)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	first := put(db, "a", "school", at)
	second := put(db, "b", "school", at)
	put(db, "c", "other", at.Add(-time.Minute))
	late, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer late.Rollback()
	put(late, "late", "school", at.Add(-time.Second))
	q := a.Request{Scopes: []a.Scope{{School: "*", Group: "*", Module: "catalog"}}, Until: at.Add(time.Hour), Limit: 1}
	page, e := repo.ReadAudit(ctx, q)
	if e != nil || len(page.Records) != 1 || page.Snapshot == "" {
		t.Fatal(page, e)
	}
	r := page.Records[0]
	if r.ID != second || r.Result != "unknown" || r.Reason != "" || r.Target != "target" {
		t.Fatal(r, first)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "DO_NOT_EXPOSE") {
		t.Fatal("private data exposed")
	}
	if e = late.Commit(); e != nil {
		t.Fatal(e)
	}
	put(db, "backdated", "school", at.Add(-time.Second))
	q.Snapshot = page.Snapshot
	q.Before = &a.Position{Time: r.CreatedAt, Source: r.Source, ID: r.ID}
	q.Limit = 101
	rest, e := repo.ReadAudit(ctx, q)
	if e != nil || len(rest.Records) != 2 || rest.Records[0].ID != first {
		t.Fatal(rest, e)
	}
	for _, v := range rest.Records {
		if v.ID == "late" || v.ID == "backdated" {
			t.Fatal(v)
		}
	}
	q.Snapshot = ""
	q.Before = nil
	q.Actor = "missing"
	empty, e := repo.ReadAudit(ctx, q)
	if e != nil || len(empty.Records) != 0 {
		t.Fatal(empty, e)
	}
	q.Actor = r.Actor
	q.Target = "target"
	q.Action = "changed"
	q.From = at
	q.Until = at.Add(time.Microsecond)
	filtered, e := repo.ReadAudit(ctx, q)
	if e != nil || len(filtered.Records) != 2 {
		t.Fatal(filtered, e)
	}
	q.Target = "missing"
	empty, e = repo.ReadAudit(ctx, q)
	if e != nil || len(empty.Records) != 0 {
		t.Fatal(empty, e)
	}
	q.Target = "target"
	q.Action = "missing"
	empty, e = repo.ReadAudit(ctx, q)
	if e != nil || len(empty.Records) != 0 {
		t.Fatal(empty, e)
	}
	q.Action = "changed"
	for _, scope := range []a.Scope{{School: "missing", Group: "*", Module: "catalog"}, {School: "*", Group: "group", Module: "catalog"}, {School: "*", Group: "*", Module: "other"}} {
		q.Scopes = []a.Scope{scope}
		empty, e = repo.ReadAudit(ctx, q)
		if e != nil || len(empty.Records) != 0 {
			t.Fatal(empty, e)
		}
	}
	q.Scopes = []a.Scope{{School: "school", Group: "*", Module: "catalog"}}
	scoped, e := repo.ReadAudit(ctx, q)
	if e != nil || len(scoped.Records) != 2 {
		t.Fatal(scoped, e)
	}
	q.Scopes = []a.Scope{{School: "missing", Group: "*", Module: "catalog"}, {School: "*", Group: "*", Module: "catalog"}}
	union, e := repo.ReadAudit(ctx, q)
	if e != nil || len(union.Records) != 2 {
		t.Fatal(union, e)
	}
	q.Before = &a.Position{Time: at, Source: "zzzz", ID: "last"}
	cross, e := repo.ReadAudit(ctx, q)
	if e != nil || len(cross.Records) != 2 {
		t.Fatal(cross, e)
	}
	q.Before.Source = "aaaa"
	cross, e = repo.ReadAudit(ctx, q)
	if e != nil || len(cross.Records) != 0 {
		t.Fatal(cross, e)
	}
	q.Before = nil
	q.Scopes = nil
	empty, e = repo.ReadAudit(ctx, q)
	if e != nil || len(empty.Records) != 0 {
		t.Fatal(empty, e)
	}
	q.Scopes = []a.Scope{{School: "*", Group: "*", Module: "catalog"}}
	q.Snapshot = page.Snapshot
	if _, e = db.Exec(`UPDATE catalog.audit SET reason='catalog.merge',result='success'`); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ReadAudit(ctx, q); e == nil {
		t.Fatal("update must invalidate snapshot")
	}
	q.Snapshot = ""
	summary, e := repo.ReadAudit(ctx, q)
	if e != nil || len(summary.Records) != 2 || summary.Records[0].Reason != "catalog.merge" || summary.Records[0].Result != "success" {
		t.Fatal(summary, e)
	}
	if _, e = db.Exec(`UPDATE catalog.audit SET reason='identity@example.invalid private content'`); e != nil {
		t.Fatal(e)
	}
	q.Snapshot = ""
	redacted, e := repo.ReadAudit(ctx, q)
	if e != nil || len(redacted.Records) != 2 || redacted.Records[0].Reason != "" {
		t.Fatal(redacted, e)
	}
	q.Snapshot = redacted.Snapshot
	if _, e = db.Exec(`DELETE FROM catalog.audit`); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ReadAudit(ctx, q); e == nil {
		t.Fatal("retention must invalidate snapshot")
	}
	q.Snapshot = "malformed"
	if _, e = repo.ReadAudit(ctx, q); e == nil {
		t.Fatal("invalid snapshot accepted")
	}
	q.Snapshot = ""
	q.Limit = 102
	if _, e = repo.ReadAudit(ctx, q); e != a.ErrInvalid {
		t.Fatal(e)
	}
	q.Limit = 1
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = repo.ReadAudit(cancelled, q); e != context.Canceled {
		t.Fatal(e)
	}
}
