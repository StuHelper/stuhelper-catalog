package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"github.com/jackc/pgx/v5/stdlib"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutboxNotificationBacklogOrderAndConfirmation(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	listener, e := repo.DB.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if _, e = listener.ExecContext(ctx, "LISTEN "+c.OutboxChannel); e != nil {
		t.Fatal(e)
	}
	publish := func(id, subject string, revision int64, rollback bool) error {
		return repo.Transaction(ctx, func(tx c.Transaction) error {
			if e := tx.Event(c.Event{ID: id, Type: "catalog.test.v1", Subject: subject, Revision: revision, Time: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}); e != nil {
				return e
			}
			if rollback {
				return errors.New("rollback")
			}
			return nil
		})
	}
	if e = publish("rollback", "object", 1, true); e == nil {
		t.Fatal("expected rollback")
	}
	noNotice, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	e = listener.Raw(func(raw any) error { _, e := raw.(*stdlib.Conn).Conn().WaitForNotification(noNotice); return e })
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("rollback notified", e)
	}
	pending, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM catalog.audit`).Scan(&count); e != nil || count != 0 {
		t.Fatal("rollback audit", count, e)
	}
	// Publish revisions out of order to distinguish numeric order from timestamps/IDs.
	for _, v := range []struct {
		id, subject string
		revision    int64
	}{{"ten", "a", 10}, {"two", "a", 2}, {"one", "a", 1}, {"z-one", "z", 1}} {
		if e = publish(v.id, v.subject, v.revision, false); e != nil {
			t.Fatal(e)
		}
	}
	notice, stop := context.WithTimeout(ctx, time.Second)
	e = listener.Raw(func(raw any) error {
		n, e := raw.(*stdlib.Conn).Conn().WaitForNotification(notice)
		if e == nil && (n.Channel != c.OutboxChannel || n.Payload != "") {
			t.Error(n)
		}
		return e
	})
	stop()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = listener.ExecContext(ctx, "UNLISTEN *"); e != nil {
		t.Fatal(e)
	}
	// Accumulate without a listener, then read through a fresh repository instance.
	for i := 0; i < 101; i++ {
		if e = publish(fmt.Sprintf("backlog-%03d", i), "backlog", int64(i+1), false); e != nil {
			t.Fatal(e)
		}
	}
	restarted := &postgres.Repository{DB: repo.DB}
	pending, e = restarted.PendingOutbox(ctx, 100, nil)
	if e != nil || len(pending) != 100 {
		t.Fatal(len(pending), e)
	}
	if pending[0].ID != "one" || pending[1].ID != "two" || pending[2].ID != "ten" {
		t.Fatal(pending[:3])
	}
	again, e := restarted.PendingOutbox(ctx, 100, nil)
	if e != nil || !reflect.DeepEqual(again, pending) {
		t.Fatal("read changed delivery", e)
	}
	for i, event := range pending {
		if event.SpecVersion != "1.0" || event.Source != "catalog" || event.Subject == "" || event.Time.IsZero() || event.Data.Revision < 1 {
			t.Fatal(event)
		}
		if i > 0 {
			prev := pending[i-1]
			if prev.Subject > event.Subject || prev.Subject == event.Subject && prev.Data.Revision > event.Data.Revision {
				t.Fatal(prev, event)
			}
		}
		if e = restarted.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
	}
	var first time.Time
	if e = db.QueryRow(`SELECT sent_at FROM catalog.outbox WHERE id=$1`, pending[0].ID).Scan(&first); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- repo.ConfirmOutbox(ctx, pending[0].ID) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var second time.Time
	if e = db.QueryRow(`SELECT sent_at FROM catalog.outbox WHERE id=$1`, pending[0].ID).Scan(&second); e != nil || !first.Equal(second) {
		t.Fatal(first, second, e)
	}
	rest, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(rest) != 5 || rest[4].ID != "z-one" {
		t.Fatal(rest, e)
	}
	for _, v := range rest {
		if e = repo.ConfirmOutbox(ctx, v.ID); e != nil {
			t.Fatal(e)
		}
	}
	empty, e := repo.PendingOutbox(ctx, 1, nil)
	if e != nil || len(empty) != 0 {
		t.Fatal(empty, e)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, e = repo.PendingOutbox(ctx, limit, nil); !errors.Is(e, c.ErrInput) {
			t.Fatal(limit, e)
		}
	}
	if e = repo.ConfirmOutbox(ctx, "missing"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	longID := "discovery:" + strings.Repeat("x", 128) + ":1"
	if e = publish(longID, "long", 1, false); e != nil {
		t.Fatal(e)
	}
	if e = repo.ConfirmOutbox(ctx, longID); e != nil {
		t.Fatal(e)
	}
	cancelled, done := context.WithCancel(ctx)
	done()
	if _, e = repo.PendingOutbox(cancelled, 1, nil); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = repo.ConfirmOutbox(cancelled, longID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	var index string
	if e = db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname='catalog' AND indexname='catalog_outbox_pending'`).Scan(&index); e != nil || !strings.Contains(index, "WHERE (sent_at IS NULL)") || !strings.Contains(index, "bigint") {
		t.Fatal(index, e)
	}
}

func TestOutboxDiscoveryAndTimetablePayloads(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	for range 2 {
		if _, e := s.Import(ctx, admin, "seed", in); e != nil {
			t.Fatal(e)
		}
		if _, e := s.PutTimetable(ctx, admin, timetable()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Delete(ctx, admin, "teacher", "delete", c.ObjectState{Revision: 1, Reason: "private reason"}); e != nil {
		t.Fatal(e)
	}
	cfg := timetable()
	cfg.Revision = 1
	cfg.Periods[0].Start = "08:10"
	if _, e := s.PutTimetable(ctx, admin, cfg); e != nil {
		t.Fatal(e)
	}
	events, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(events) != 9 {
		t.Fatal(len(events), e)
	}
	discovery, timetables, tombstones := 0, 0, 0
	for _, event := range events {
		// Marshaling a typed relay envelope must preserve the complete stored payload.
		var raw []byte
		if e = repo.DB.QueryRow(`SELECT data FROM catalog.outbox WHERE id=$1`, event.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(event)
		if e != nil {
			t.Fatal(e)
		}
		var stored, returned any
		if e = json.Unmarshal(raw, &stored); e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(encoded, &returned); e != nil || !reflect.DeepEqual(stored, returned) {
			t.Fatal(string(raw), string(encoded), e)
		}
		if strings.Contains(string(encoded), "private reason") || strings.Contains(string(encoded), "issuer:") {
			t.Fatal("private fields leaked", string(encoded))
		}
		switch event.Type {
		case "catalog.discovery.v1":
			discovery++
			if event.Data.Target == nil || event.Subject != event.Data.Target.ID || event.Data.Title == nil || event.Data.SchoolID == nil || event.Data.Visible == nil || event.Data.Ratings == nil || event.Data.At == nil {
				t.Fatal(event)
			}
			if !*event.Data.Visible {
				tombstones++
				if *event.Data.Title != "" || event.Data.Revision != 2 {
					t.Fatal(event)
				}
			}
		case "catalog.timetable.updated.v1":
			timetables++
			if event.Subject != "timetable/school/term1" || event.Data.Revision != int64(timetables) {
				t.Fatal(event)
			}
		}
		if e = repo.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
	}
	if discovery != 5 || timetables != 2 || tombstones != 1 {
		t.Fatal(discovery, timetables, tombstones)
	}
	empty, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(empty) != 0 {
		t.Fatal(empty, e)
	}
}

func TestOutboxMergeRevisionBelongsToSourceSubject(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects, c.Object{ID: "duplicate", Kind: "course", SchoolID: "school", Name: "Duplicate"})
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	name := "Target updated"
	if _, e := s.Correct(ctx, p, "course", "correct", c.Correction{Revision: 1, Name: &name}); e != nil {
		t.Fatal(e)
	}
	input := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 2, Reason: "Confirmed duplicate"}
	preview, e := s.PreviewMerge(ctx, p, "duplicate", input)
	if e != nil {
		t.Fatal(e)
	}
	input.PreviewToken = preview.PreviewToken
	if _, e = s.Merge(ctx, p, "duplicate", "merge", input); e != nil {
		t.Fatal(e)
	}
	pending, e := s.Repo.(c.OutboxReader).PendingOutbox(ctx, 100, nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range pending {
		if v.Type == "catalog.merged.v1" {
			found = true
			if v.Subject != "duplicate" || v.Data.Revision != 2 {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal("merge event missing")
	}
}

func TestOutboxExcludedSubjectsDoNotBlockOtherSubjects(t *testing.T) {
	repo, _ := database(t)
	ctx := context.Background()
	err := repo.Transaction(ctx, func(tx c.Transaction) error {
		for i := 1; i <= 100; i++ {
			if e := tx.Event(c.Event{ID: fmt.Sprintf("blocked-%03d", i), Type: "catalog.test.v1", Subject: "a-blocked", Revision: int64(i), Time: time.Now().UTC()}); e != nil {
				return e
			}
		}
		for _, v := range []struct {
			id, subject string
			rev         int64
		}{{"z-ten", "z-ready", 10}, {"z-two-b", "z-ready", 2}, {"z-two-a", "z-ready", 2}, {"b-one", "b-ready", 1}} {
			if e := tx.Event(c.Event{ID: v.id, Type: "catalog.test.v1", Subject: v.subject, Revision: v.rev, Time: time.Now().UTC()}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(baseline) != 100 || baseline[99].Subject != "a-blocked" {
		t.Fatal(baseline, e)
	}
	empty, e := repo.PendingOutbox(ctx, 100, []string{})
	if e != nil || !reflect.DeepEqual(empty, baseline) {
		t.Fatal("empty exclusion changed results", e)
	}
	result, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked"})
	if e != nil || len(result) != 4 {
		t.Fatal(result, e)
	}
	want := []string{"b-one", "z-two-a", "z-two-b", "z-ten"}
	for i, v := range result {
		if v.ID != want[i] {
			t.Fatal(result)
		}
	}
	repeat, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked", "a-blocked", "missing"})
	if e != nil || !reflect.DeepEqual(repeat, result) {
		t.Fatal(repeat, e)
	}
	one, e := repo.PendingOutbox(ctx, 1, []string{"a-blocked"})
	if e != nil || len(one) != 1 || one[0].ID != "b-one" {
		t.Fatal(one, e)
	}
	all, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked", "b-ready", "z-ready"})
	if e != nil || len(all) != 0 {
		t.Fatal(all, e)
	}
	maximum := make([]string, 1000)
	for i := range maximum {
		maximum[i] = fmt.Sprintf("absent-%d", i)
	}
	maximum[999] = "a-blocked"
	boundary, e := repo.PendingOutbox(ctx, 100, maximum)
	if e != nil || !reflect.DeepEqual(boundary, result) {
		t.Fatal(boundary, e)
	}
	if _, e = repo.PendingOutbox(ctx, 100, append(maximum, "overflow")); !errors.Is(e, c.ErrInput) {
		t.Fatal("unbounded exclusion accepted", e)
	}
	// Exclusion has no delivery side effect: removing it makes the backlog visible.
	restored, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || !reflect.DeepEqual(restored, baseline) {
		t.Fatal("excluded events changed", e)
	}
}

func TestBoundedPruningHostAllowlistAndOperationalMetrics(t *testing.T) {
	s, p, in := fixture(t)
	s.ReadAccess = &readAccess{}
	p.CanReadTeacherSites = true
	p.CanRefreshTeacherSites = true
	repo := s.Repo.(*postgres.Repository)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }

	for i := range in.Objects {
		if in.Objects[i].ID == "teacher" {
			in.Objects[i].DepartmentID = "dept"
		}
	}
	in.Objects = append(in.Objects,
		c.Object{ID: "course-old", Kind: "course", SchoolID: "school", Name: "Old Code Course", Code: "OLD1", Aliases: []string{}},
		c.Object{ID: "course-new", Kind: "course", SchoolID: "school", Name: "New Code Course", Code: "NEW1", Aliases: []string{}},
	)
	in.FetchedAt = now
	if _, err := s.Import(ctx, p, "durable-import", in); err != nil {
		t.Fatal(err)
	}
	identities := c.CourseIdentities{
		SchoolID: "school", SourceID: in.SourceID, Revision: in.Revision,
		Mappings: []c.CourseIdentityMapping{{SourceID: "course-old", TargetID: "course-new"}},
	}
	if _, err := s.ApplyCourseIdentities(ctx, p, "durable-identities", identities); err != nil {
		t.Fatal(err)
	}

	// Create 3 expiring maintenance replays via Correct.
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("Course v%d", i)
		if _, err := s.Correct(ctx, p, "course", fmt.Sprintf("edit-%d", i), c.Correction{Revision: int64(i), Name: &name}); err != nil {
			t.Fatal(err)
		}
	}

	// Input bounds validation across all 3 prune ports.
	for _, badLimit := range []int{0, 1001} {
		if _, err := repo.PruneReplays(ctx, now, badLimit); !errors.Is(err, c.ErrInput) {
			t.Fatalf("PruneReplays limit %d: %v", badLimit, err)
		}
		if _, err := repo.PruneSentOutbox(ctx, now, badLimit); !errors.Is(err, c.ErrInput) {
			t.Fatalf("PruneSentOutbox limit %d: %v", badLimit, err)
		}
		if _, err := repo.PruneTeacherRefreshRuns(ctx, now, badLimit); !errors.Is(err, c.ErrInput) {
			t.Fatalf("PruneTeacherRefreshRuns limit %d: %v", badLimit, err)
		}
	}
	if _, err := repo.PruneReplays(ctx, time.Time{}, 10); !errors.Is(err, c.ErrInput) {
		t.Fatal("zero before accepted", err)
	}

	// Advance 30 days: bounded PruneReplays deletes only expiring maintenance replays, never durable Import/CourseIdentities receipts.
	now = now.Add(30 * 24 * time.Hour)
	if n, err := s.PruneReplays(ctx, 2); err != nil || n != 2 {
		t.Fatalf("first PruneReplays batch: n=%d err=%v", n, err)
	}
	if n, err := s.PruneReplays(ctx, 10); err != nil || n != 1 {
		t.Fatalf("second PruneReplays batch: n=%d err=%v", n, err)
	}
	if _, err := s.Import(ctx, p, "durable-import", in); err != nil {
		t.Fatalf("durable Import receipt was pruned: %v", err)
	}
	if _, err := s.ApplyCourseIdentities(ctx, p, "durable-identities", identities); err != nil {
		t.Fatalf("durable ApplyCourseIdentities receipt was pruned: %v", err)
	}

	// PruneSentOutbox deletes only confirmed rows older than cutoff, sparing pending and newer confirmed rows.
	pending, err := repo.PendingOutbox(ctx, 100, nil)
	if err != nil || len(pending) < 4 {
		t.Fatalf("pending outbox: %d %v", len(pending), err)
	}
	for i := 0; i < 3; i++ {
		if err := repo.ConfirmOutbox(ctx, pending[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	sentCutoff := time.Now().UTC().Add(-time.Hour)
	if _, err := repo.DB.ExecContext(ctx, `UPDATE catalog.outbox SET sent_at=$1 WHERE id IN ($2,$3)`, sentCutoff.Add(-time.Minute), pending[0].ID, pending[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB.ExecContext(ctx, `UPDATE catalog.outbox SET sent_at=$1 WHERE id=$2`, sentCutoff.Add(time.Minute), pending[2].ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.PruneSentOutbox(ctx, sentCutoff, 1); err != nil || n != 1 {
		t.Fatalf("first PruneSentOutbox: n=%d err=%v", n, err)
	}
	if n, err := repo.PruneSentOutbox(ctx, sentCutoff, 10); err != nil || n != 1 {
		t.Fatalf("second PruneSentOutbox: n=%d err=%v", n, err)
	}
	stillPending, err := repo.PendingOutbox(ctx, 100, nil)
	if err != nil || len(stillPending) != len(pending)-3 {
		t.Fatalf("pending outbox altered by prune: got=%d want=%d err=%v", len(stillPending), len(pending)-3, err)
	}

	// TeacherSiteHosts per-school allowlist validation.
	candidates, err := s.TeacherCandidates(ctx, p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	fp := c.TeacherCandidateFingerprint(candidates)
	s.TeacherSiteHosts = map[string][]string{"school": {"faculty.example.edu.cn"}}
	if _, err := s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{
		CandidateFingerprint: fp, SchoolID: "school", SourceID: "sites", Revision: 1, ObservedAt: now,
		Matches: []c.TeacherSiteMatch{{TeacherID: "teacher", Name: "teacher", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/t1/index.htm"}},
	}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("disallowed default host accepted when custom allowlist configured: %v", err)
	}
	if _, err := s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{
		CandidateFingerprint: fp, SchoolID: "school", SourceID: "sites", Revision: 1, ObservedAt: now,
		Matches: []c.TeacherSiteMatch{{TeacherID: "teacher", Name: "teacher", DepartmentID: "dept", URL: "https://faculty.example.edu.cn/t1/index.htm"}},
	}); err != nil {
		t.Fatalf("configured school host rejected: %v", err)
	}

	// PruneTeacherRefreshRuns deletes older terminal runs while preserving the latest run per school and any active run.
	for i := 1; i <= 3; i++ {
		run, err := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", fmt.Sprintf("run-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimTeacherRefresh(ctx, p, "school"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CompleteTeacherRefresh(ctx, p, run.ID, c.TeacherRefreshCompletion{FailureReason: "crawl_failed"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Hour)
	}
	if n, err := repo.PruneTeacherRefreshRuns(ctx, now, 10); err != nil || n != 2 {
		t.Fatalf("PruneTeacherRefreshRuns should delete 2 older terminal runs and keep latest: n=%d err=%v", n, err)
	}
	status, err := s.TeacherRefreshStatus(ctx, p, "school")
	if err != nil || status.Latest == nil {
		t.Fatalf("latest refresh run lost after prune: %+v %v", status, err)
	}
	queued, err := s.QueueTeacherRefresh(ctx, p, "school", "full", "manual", "active-run")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if n, err := repo.PruneTeacherRefreshRuns(ctx, now.Add(-time.Minute), 10); err != nil || n != 1 {
		t.Fatalf("previous terminal run should prune once newer queued run becomes latest, while keeping queued run: n=%d err=%v", n, err)
	}
	claimed, err := s.ClaimTeacherRefresh(ctx, p, "school")
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("active queued run removed by prune: %+v %v", claimed, err)
	}

	// DirectoryCacheMetrics & OperationalMetrics verification.
	if err := s.SaveTermOrders(ctx, p, "school", []c.TermOrder{{ID: "term1", Year: 2024, Season: "autumn"}, {ID: "term2", Year: 2025, Season: "spring"}}); err != nil {
		t.Fatal(err)
	}
	repo.RequireDirectoryProjection = true
	s.DirectoryMetrics = uiMetrics{}
	builtAt := now
	s.Clock = func() time.Time { return builtAt }
	report, err := s.RebuildDirectory(ctx, p, "school", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Trigger 1 miss + 1 hit on default course query, then 4 more distinct queries to trigger eviction (cache capacity = 4).
	student := c.Principal{Subject: "issuer:student"}
	if _, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 10}, Sort: "name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 10}, Sort: "name"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Query: fmt.Sprintf("q%d", i), Limit: 10}, Sort: "name"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.DB.ExecContext(ctx, `INSERT INTO catalog.projection_failures(school_id,failed_at,reason) VALUES('school',$1,'build_timeout') ON CONFLICT(school_id) DO UPDATE SET failed_at=excluded.failed_at,reason=excluded.reason`, builtAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	s.Clock = func() time.Time { return builtAt.Add(90 * time.Second) }
	metrics, err := s.OperationalMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Cache.Misses != 5 || metrics.Cache.Hits != 1 || metrics.Cache.Evictions < 1 || metrics.Cache.Entries != 4 {
		t.Fatalf("unexpected cache metrics: %+v", metrics.Cache)
	}
	if metrics.PendingOutbox < 1 || len(metrics.Projections) != 1 {
		t.Fatalf("unexpected operational metrics: %+v", metrics)
	}
	proj := metrics.Projections[0]
	if proj.SchoolID != "school" || proj.Version != report.Version || proj.AgeSeconds != 90 || proj.LastFailureReason != "build_timeout" || proj.LastFailedAt == nil {
		t.Fatalf("unexpected projection metrics: %+v", proj)
	}
}
