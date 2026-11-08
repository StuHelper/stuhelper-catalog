package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	a "github.com/StuHelper/stuhelper-catalog/auditquery"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"github.com/jackc/pgx/v5/stdlib"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryEventsRollbackMergeDeletionAndCurrent(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	in.Objects = append(in.Objects, c.Object{ID: "course-other", Kind: "course", SchoolID: "school", Name: "Other"})
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.discovery.v1'`).Scan(&count); e != nil || count != 4 {
		t.Fatal(count, e)
	}
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	if e := repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.discovery.v1'`).Scan(&count); e != nil || count != 4 {
		t.Fatal(count, e)
	}
	name := "new title"
	obj, e := s.Correct(ctx, p, "course", "correct", c.Correction{Revision: 1, Name: &name})
	if e != nil {
		t.Fatal(e)
	}
	merge := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: obj.Revision, Reason: "private reason identity@example.invalid"}
	preview, e := s.PreviewMerge(ctx, p, "course-other", merge)
	if e != nil {
		t.Fatal(e)
	}
	merge.PreviewToken = preview.PreviewToken
	obj, e = s.Merge(ctx, p, "course-other", "merge", merge)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Delete(ctx, p, "teacher", "delete", c.ObjectState{Revision: 1, Reason: "remove private content"}); e != nil {
		t.Fatal(e)
	}
	current, e := s.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"course", "course-other", "teacher", "teacher2", "missing"})
	if e != nil || len(current) != 2 || current[0].Target.ID != "course" || current[0].Revision != obj.Revision {
		t.Fatal(current, e)
	}
	s.ReadAccess = &readAccess{revoked: true}
	current, e = s.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"course"})
	if e != nil || len(current) != 0 {
		t.Fatal(current, e)
	}
	s.ReadAccess = &readAccess{unavailable: true}
	if _, e = s.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"course"}); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = s.DiscoveryCurrent(ctx, c.Principal{}, []string{"course"}); !errors.Is(e, c.ErrUnauthenticated) {
		t.Fatal(e)
	}
	page, e := repo.DiscoverySnapshot(ctx, "", 100)
	if e != nil || len(page.Items) != 4 {
		t.Fatal(page, e)
	}
	for _, d := range page.Items {
		if d.Target.ID == "course-other" || d.Target.ID == "teacher" {
			if d.Visible || d.Revision != 2 || d.Title != "" {
				t.Fatal(d)
			}
		}
	}
	// Every event carries only the discovery schema fields, and its data equals history.
	rows, e := repo.DB.Query(`SELECT o.data,h.data FROM catalog.outbox o JOIN catalog.discovery_history h ON h.id=o.data->>'subject' AND h.revision=(o.data->'data'->>'revision')::bigint WHERE o.data->>'type'='catalog.discovery.v1'`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var raw, history []byte
		if e = rows.Scan(&raw, &history); e != nil {
			t.Fatal(e)
		}
		var event map[string]json.RawMessage
		if e = json.Unmarshal(raw, &event); e != nil {
			t.Fatal(e)
		}
		if len(event) != 7 {
			t.Fatal(string(raw))
		}
		var d c.DiscoveryDocument
		if e = json.Unmarshal(event["data"], &d); e != nil {
			t.Fatal(e)
		}
		var stored c.DiscoveryDocument
		_ = json.Unmarshal(history, &stored)
		if d != stored || d.Target.Source != "catalog" || d.Ratings != [5]int{} || d.At.IsZero() {
			t.Fatal(d, stored)
		}
		if strings.Contains(string(raw), "issuer:") || strings.Contains(string(raw), "private") {
			t.Fatal(string(raw))
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	// A late validation failure rolls back previously saved objects and their events.
	failed := in
	failed.Revision = 2
	failed.Objects = []c.Object{{ID: "rollback-course", Kind: "course", SchoolID: "school", Name: "Rollback"}}
	failed.Enrollments = []c.Enrollment{{ID: "bad", Subject: "s", SchoolID: "school", CourseID: "missing", TermID: "term1", Active: true}}
	if _, e = s.Import(ctx, p, "bad", failed); e == nil {
		t.Fatal("invalid import accepted")
	}
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.discovery_history WHERE id='rollback-course'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'subject'='rollback-course'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	audits, e := repo.ReadAudit(ctx, a.Request{Scopes: []a.Scope{{School: "school", Group: "*", Module: "catalog"}}, Until: time.Now().Add(time.Minute), Limit: 101})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range audits.Records {
		if r.Result != "success" {
			t.Fatal(r)
		}
		if strings.Contains(r.Reason, "private") || strings.Contains(r.Reason, "identity@") {
			t.Fatal(r)
		}
	}
}

func TestDiscoverySnapshotStableWithUpdatesLateCommitsAndRetention(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	// An uncommitted new ID that sorts after page one cannot enter the snapshot later.
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- repo.Transaction(ctx, func(tx c.Transaction) error {
			if e := tx.SaveObject(c.Record{Object: c.Object{ID: "late-teacher", Kind: "teacher", SchoolID: "school", Name: "Late", Revision: 1, UpdatedAt: time.Now()}}); e != nil {
				return e
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case e := <-done:
		t.Fatal(e)
	}
	page, e := repo.DiscoverySnapshot(ctx, "", 1)
	if e != nil || len(page.Items) != 1 || page.Next == "" {
		close(release)
		t.Fatal(page, e)
	}
	firstCursor := page.Next
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = s.Delete(ctx, p, "teacher", "delete", c.ObjectState{Revision: 1, Reason: "deleted"}); e != nil {
		t.Fatal(e)
	}
	seen := map[string]c.DiscoveryDocument{page.Items[0].Target.ID: page.Items[0]}
	for page.Next != "" {
		old := page.Next
		page, e = repo.DiscoverySnapshot(ctx, page.Next, 1)
		if e != nil || page.Next == old {
			t.Fatal(page, e)
		}
		for _, d := range page.Items {
			if _, ok := seen[d.Target.ID]; ok {
				t.Fatal("duplicate")
			}
			seen[d.Target.ID] = d
		}
	}
	if len(seen) != 3 || !seen["teacher"].Visible || seen["teacher"].Revision != 1 {
		t.Fatal(seen)
	}
	fresh, e := repo.DiscoverySnapshot(ctx, "", 100)
	if e != nil || len(fresh.Items) != 4 {
		t.Fatal(fresh, e)
	}
	if _, e = repo.DB.Exec(`DELETE FROM catalog.discovery_history WHERE id='teacher' AND revision=1`); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.DiscoverySnapshot(ctx, firstCursor, 1); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("retained history was silently omitted", e)
	}
	if _, e = repo.DiscoverySnapshot(ctx, "bad", 1); e == nil {
		t.Fatal("bad cursor accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = repo.DiscoverySnapshot(cancelled, "", 1); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestDiscoveryNotificationAndAuditAtomicity(t *testing.T) {
	repo, migration := database(t)
	ctx := context.Background()
	s := &c.Service{Repo: repo, ReadAccess: &readAccess{}}
	p := c.Principal{Subject: "admin", Schools: []string{"school", "elsewhere"}, CanImport: true, CanMaintain: true}
	for _, school := range []string{"school", "elsewhere"} {
		in := c.Import{SchoolID: school, SourceID: "source", Revision: 1, FetchedAt: time.Now(), Objects: []c.Object{{ID: school, Kind: "school", SchoolID: school, Name: school}, {ID: school + "-teacher", Kind: "teacher", SchoolID: school, Name: "Teacher"}}}
		if _, e := s.Import(ctx, p, "seed-"+school, in); e != nil {
			t.Fatal(e)
		}
	}
	visible, e := s.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"school-teacher", "elsewhere-teacher"})
	if e != nil || len(visible) != 1 || visible[0].Target.ID != "school-teacher" {
		t.Fatal(visible, e)
	}
	listener, e := repo.DB.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if _, e = listener.ExecContext(ctx, `LISTEN catalog_outbox`); e != nil {
		t.Fatal(e)
	}
	if _, e = migration.Exec(`CREATE FUNCTION catalog.fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER fail_audit BEFORE INSERT ON catalog.audit FOR EACH ROW EXECUTE FUNCTION catalog.fail_audit()`); e != nil {
		t.Fatal(e)
	}
	title := "Updated"
	if _, e = s.Correct(ctx, p, "school-teacher", "correct", c.Correction{Revision: 1, Name: &title}); e == nil {
		t.Fatal("audit failure committed")
	}
	var n int
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.discovery_history WHERE id='school-teacher'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'subject'='school-teacher'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	noNotification, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	e = listener.Raw(func(raw any) error { _, e := raw.(*stdlib.Conn).Conn().WaitForNotification(noNotification); return e })
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("rollback notified", e)
	}
	if _, e = migration.Exec(`DROP TRIGGER fail_audit ON catalog.audit`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Correct(ctx, p, "school-teacher", "correct", c.Correction{Revision: 1, Name: &title}); e != nil {
		t.Fatal(e)
	}
	notification, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	e = listener.Raw(func(raw any) error {
		n, e := raw.(*stdlib.Conn).Conn().WaitForNotification(notification)
		if e == nil && n.Channel != "catalog_outbox" {
			t.Error(n)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	audit, e := repo.ReadAudit(ctx, a.Request{Scopes: []a.Scope{{School: "school", Group: "*", Module: "catalog"}}, Action: "catalog.corrected.v1", Target: "school-teacher", Until: time.Now().Add(time.Minute), Limit: 10})
	if e != nil || len(audit.Records) != 1 || audit.Records[0].Result != "success" || audit.Records[0].Reason != "" {
		t.Fatal(audit, e)
	}
	if e = repo.Transaction(ctx, func(tx c.Transaction) error {
		return tx.Event(c.Event{ID: "unknown-event", Revision: 1, Actor: "admin", Subject: "school-teacher", SchoolID: "school", Type: "unproven", Time: time.Now(), Reason: "identity@example.invalid"})
	}); e != nil {
		t.Fatal(e)
	}
	audit, e = repo.ReadAudit(ctx, a.Request{Scopes: []a.Scope{{School: "school", Group: "*", Module: "catalog"}}, Action: "unproven", Until: time.Now().Add(time.Minute), Limit: 10})
	if e != nil || len(audit.Records) != 1 || audit.Records[0].Result != "unknown" || audit.Records[0].Reason != "" {
		t.Fatal(audit, e)
	}
}
