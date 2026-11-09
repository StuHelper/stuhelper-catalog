package catalog_test

import (
	"bytes"
	"context"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type viewAccess struct{}

func (viewAccess) Scope(_ context.Context, p c.Principal, school string) (string, error) {
	if p.Subject == "" || school != "" && school != "school" {
		return "", c.ErrForbidden
	}
	return "school", nil
}

func TestActivityViewsDeduplicateConcurrentReadersAndDoNotBreakDetails(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	s.ReadAccess = viewAccess{}
	repo := s.Repo.(*postgres.Repository)
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return day.Add(time.Hour) }
	viewer := c.Principal{Subject: "private-viewer"}
	o, e := s.Read(ctx, viewer, "course")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadSummary(ctx, viewer, "course"); e != nil {
		t.Fatal(e)
	}
	id := c.ActivityID("course", "course", day)
	d, e := repo.ReadActivity(ctx, id)
	if e != nil || d.Metrics["views"] != 1 || d.Revision != 1 {
		t.Fatal(d, e)
	}
	stats := s.ViewMetrics()
	if stats.Recorded != 1 || stats.Duplicates != 1 || stats.Failures != 0 {
		t.Fatal(stats)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			owner := viewer.Subject
			if i >= 8 {
				owner = fmt.Sprintf("reader-%d", i)
			}
			errs <- repo.Transaction(ctx, func(tx c.Transaction) error { _, e := tx.RecordView(owner, o, day.Add(time.Hour)); return e })
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	d, e = repo.ReadActivity(ctx, id)
	if e != nil || d.Metrics["views"] != 9 || d.Revision != 9 {
		t.Fatal(d, e)
	}
	if _, ok, e := s.CurrentActivity(ctx, viewer, id); e != nil || !ok {
		t.Fatal(ok, e)
	}
	again, e := repo.ReadActivity(ctx, id)
	if e != nil || again.Revision != d.Revision {
		t.Fatal("current activity counted another view", again, e)
	}
	s.ReadAccess = &readAccess{revoked: true}
	if _, ok, e := s.CurrentActivity(ctx, viewer, id); e != nil || ok {
		t.Fatal(ok, e)
	}
	s.ReadAccess = viewAccess{}
	if _, e = s.Read(ctx, c.Principal{}, "course"); e == nil {
		t.Fatal("anonymous detail accepted")
	}
	// Cancellation at the repository boundary cannot commit a view.
	before := s.ViewMetrics()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	// Direct cancellation is tested at the repository boundary; it cannot commit a view.
	if e = repo.Transaction(cancelled, func(tx c.Transaction) error { _, e := tx.RecordView("cancelled", o, day); return e }); e == nil {
		t.Fatal("cancelled write committed")
	}
	if s.ViewMetrics() != before {
		t.Fatal("repository calls changed service metrics")
	}
}

func TestActivityViewFailureTelemetryAndBoundedDedupRetention(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	s := &c.Service{Repo: repo, ReadAccess: viewAccess{}}
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	now := day
	s.Clock = func() time.Time { return now }
	if e := repo.Transaction(ctx, func(tx c.Transaction) error {
		return tx.SaveObject(c.Record{Object: c.Object{ID: "course", Kind: "course", SchoolID: "school", Name: "Public title", Revision: 1, UpdatedAt: day}})
	}); e != nil {
		t.Fatal(e)
	}
	var runtimeRole string
	if e := repo.DB.QueryRow(`SELECT current_user`).Scan(&runtimeRole); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`REVOKE UPDATE ON catalog.view_retention FROM ` + runtimeRole); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	s.ActivityLogger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, e := db.Exec(`ALTER TABLE catalog.outbox ADD CONSTRAINT fail_views CHECK(data->>'type'<>'discovery.activity.v1') NOT VALID`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read(ctx, c.Principal{Subject: "private-user"}, "course"); e != nil {
		t.Fatal("analytics failure broke detail", e)
	}
	if s.ViewMetrics().Failures != 1 || !strings.Contains(logs.String(), "activity.view") || strings.Contains(logs.String(), "private-user") || strings.Contains(logs.String(), "fail_views") {
		t.Fatal(s.ViewMetrics(), logs.String())
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM catalog.view_dedup`).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed write kept dedup", n, e)
	}
	if _, e := db.Exec(`ALTER TABLE catalog.outbox DROP CONSTRAINT fail_views`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Read(ctx, c.Principal{Subject: "private-user"}, "course"); e != nil {
		t.Fatal(e)
	}
	for i := range 5 {
		now = day.AddDate(0, 0, -40-i)
		if _, e := s.Read(ctx, c.Principal{Subject: "old-user"}, "course"); e != nil {
			t.Fatal(e)
		}
	}
	removed, e := (&postgres.Repository{DB: db}).PruneViewDedup(ctx, day, 2)
	if e != nil || removed != 2 {
		t.Fatal(removed, e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM catalog.view_dedup`).Scan(&n); e != nil || n != 4 {
		t.Fatal(n, e)
	}
	now = day.AddDate(0, 0, -40)
	before := s.ViewMetrics().Failures
	if _, e = s.Read(ctx, c.Principal{Subject: "old-user"}, "course"); e != nil {
		t.Fatal(e)
	}
	if s.ViewMetrics().Failures != before+1 {
		t.Fatal("cleaned date accepted backfill")
	}
	var columns string
	if e = db.QueryRow(`SELECT string_agg(column_name,',' ORDER BY column_name) FROM information_schema.columns WHERE table_schema='catalog' AND table_name='view_dedup'`).Scan(&columns); e != nil || columns != "day,digest" {
		t.Fatal(columns, e)
	}
	now = day
	if e = repo.Transaction(ctx, func(tx c.Transaction) error {
		r, e := tx.Object("course")
		if e != nil {
			return e
		}
		r.Object.Inactive = true
		r.Object.Revision++
		return tx.SaveObject(r)
	}); e != nil {
		t.Fatal(e)
	}
	docs, _, e := repo.ActivitySnapshot(ctx, day, day.AddDate(0, 0, 1), "", 100)
	if e != nil || len(docs) != 1 || docs[0].Visible {
		t.Fatal(docs, e)
	}
	if _, ok, e := s.CurrentActivity(ctx, c.Principal{Subject: "reader"}, c.ActivityID("course", "course", day)); e != nil || ok {
		t.Fatal(ok, e)
	}
}

func TestActivityViewAliasesShareCanonicalDeduplication(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects, c.Object{ID: "old-course", Kind: "course", SchoolID: "school", Name: "Old course"})
	if _, e := s.Import(ctx, p, "seed-alias", in); e != nil {
		t.Fatal(e)
	}
	merge := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "duplicate"}
	preview, e := s.PreviewMerge(ctx, p, "old-course", merge)
	if e != nil {
		t.Fatal(e)
	}
	merge.PreviewToken = preview.PreviewToken
	if _, e = s.Merge(ctx, p, "old-course", "merge", merge); e != nil {
		t.Fatal(e)
	}
	viewer := c.Principal{Subject: "issuer:student"}
	if _, e = s.Read(ctx, viewer, "old-course"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read(ctx, viewer, "course"); e != nil {
		t.Fatal(e)
	}
	repo := s.Repo.(*postgres.Repository)
	docs, _, e := repo.ActivitySnapshot(ctx, time.Now().UTC().Truncate(24*time.Hour), time.Now().UTC().Truncate(24*time.Hour).Add(24*time.Hour), "", 100)
	if e != nil || len(docs) != 1 || docs[0].Target.ID != "course" || docs[0].Metrics["views"] != 1 {
		t.Fatal(docs, e)
	}
}
