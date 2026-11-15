package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func refreshFixture(t *testing.T) (*c.Service, c.Principal, c.TeacherSiteSnapshot) {
	t.Helper()
	s, p, snapshot := teacherDirectoryFixture(t)
	p.CanReadTeacherSites = true
	p.CanRefreshTeacherSites = true
	return s, p, snapshot
}
func completeRefresh(t *testing.T, s *c.Service, p c.Principal, run c.TeacherRefreshRun, snapshot c.TeacherSiteSnapshot) {
	t.Helper()
	requests, count := int64(8), int64(5)
	unmatched := []c.UnmatchedTeacherSite{{SiteID: "1", Name: "Synthetic One", Unit: "Unit A", Reason: "unit_mismatch", CandidateCount: 2}, {SiteID: "2", Name: "Synthetic Two", Unit: "Unit B", Reason: "catalog_name_absent", CandidateCount: 0}, {SiteID: "3", Name: "Synthetic Three", Unit: "Unit C", Reason: "unit_mismatch", CandidateCount: 1}}
	result, e := s.CompleteTeacherRefresh(context.Background(), p, run.ID, c.TeacherRefreshCompletion{Snapshot: &snapshot, Unmatched: unmatched, Requests: &requests, SiteCount: &count})
	if e != nil || result.Status != "succeeded" || result.Counts == nil || result.Counts.Eligible != 3 || *result.Requests != 8 {
		t.Fatal(result, e)
	}
}
func TestTeacherRefreshDurableConcurrencyIdempotencyAndTimeout(t *testing.T) {
	s, p, snapshot := refreshFixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	now := time.Now().UTC()
	s.Clock = func() time.Time { return now }
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []c.TeacherRefreshRun{}
	for i := range 12 {
		wg.Go(func() {
			v, e := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", fmt.Sprintf("request-%d", i))
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				accepted = append(accepted, v)
			} else if !errors.Is(e, c.ErrConflict) {
				t.Errorf("queue error %v", e)
			}
		})
	}
	wg.Wait()
	if len(accepted) != 1 {
		t.Fatal("multiple active jobs", accepted)
	}
	run := accepted[0]
	var active int
	if e := repo.DB.QueryRow(`SELECT count(*) FROM catalog.teacher_refresh_runs WHERE status IN ('queued','running')`).Scan(&active); e != nil || active != 1 {
		t.Fatal(active, e)
	}
	// Independent Service instance proves the lock is in the database.
	other := &c.Service{Repo: &postgres.Repository{DB: repo.DB}, Clock: s.Clock}
	if _, e := other.QueueTeacherRefresh(ctx, p, "school", "full", "manual", "competitor"); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	claimed, e := s.ClaimTeacherRefresh(ctx, p, "school")
	if e != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatal(claimed, e)
	}
	if again, e := other.ClaimTeacherRefresh(ctx, p, "school"); e != nil || again != nil {
		t.Fatal("double claim", again, e)
	}
	completeRefresh(t, s, p, run, snapshot)
	next, e := s.QueueTeacherRefresh(ctx, p, "school", "full", "manual", "stable")
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.QueueTeacherRefresh(ctx, p, "school", "full", "manual", "stable")
	if e != nil || replay.ID != next.ID {
		t.Fatal("idempotency", replay, e)
	}
	if _, e = s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", "stable"); !errors.Is(e, c.ErrConflict) {
		t.Fatal("changed body accepted", e)
	}
	if _, e = s.ClaimTeacherRefresh(ctx, p, "school"); e != nil {
		t.Fatal(e)
	}
	now = now.Add(c.TeacherRefreshTimeout + time.Second)
	status, e := s.TeacherRefreshStatus(ctx, p, "school")
	if e != nil || status.Latest.Status != "timed_out" || status.Latest.FailureReason != "timeout" {
		t.Fatal(status, e)
	}
	if _, e = s.CompleteTeacherRefresh(ctx, p, next.ID, c.TeacherRefreshCompletion{FailureReason: "worker_interrupted"}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("late worker published", e)
	}
	latest, e := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "scheduled", "monthly")
	if e != nil || latest.Status != "queued" {
		t.Fatal(latest, e)
	}
	if _, e = s.QueueTeacherRefresh(ctx, p, "school", "full", "scheduled", "bad-full"); !errors.Is(e, c.ErrInput) {
		t.Fatal("scheduled full accepted", e)
	}
}

func TestTeacherRefreshUnmatchedPaginationPrivacyAndFailureRetention(t *testing.T) {
	s, p, snapshot := refreshFixture(t)
	ctx := context.Background()
	run, e := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", "one")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimTeacherRefresh(ctx, p, "school"); e != nil {
		t.Fatal(e)
	}
	completeRefresh(t, s, p, run, snapshot)
	page, e := s.ReadUnmatchedTeacherSites(ctx, p, "school", "", "", 2)
	if e != nil || page.Total != 3 || len(page.Items) != 2 || page.Next == "" || page.ReasonCounts["unit_mismatch"] != 2 {
		t.Fatal(page, e)
	}
	tail, e := s.ReadUnmatchedTeacherSites(ctx, p, "school", "", page.Next, 2)
	if e != nil || len(tail.Items) != 1 {
		t.Fatal(tail, e)
	}
	seenSites := map[string]bool{}
	for _, site := range append(append([]c.UnmatchedTeacherSite{}, page.Items...), tail.Items...) {
		if !strings.HasPrefix(site.SiteID, "site-") || len(site.SiteID) != 69 || seenSites[site.SiteID] {
			t.Fatal("raw or duplicate website identifier", site.SiteID)
		}
		seenSites[site.SiteID] = true
	}
	selected, e := s.ReadUnmatchedTeacherSites(ctx, p, "school", "unit_mismatch", "", 20)
	if e != nil || selected.Total != 2 || len(selected.Items) != 2 || len(selected.ReasonCounts) != 2 {
		t.Fatal(selected, e)
	}
	if _, e = s.ReadUnmatchedTeacherSites(ctx, p, "school", "unit_mismatch", page.Next, 2); !errors.Is(e, c.ErrInput) {
		t.Fatal("cursor escaped filter", e)
	}
	raw, _ := json.Marshal(page)
	for _, forbidden := range []string{"teacher_id", "staff", "phone", "email", "contact", "candidates"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("private field", forbidden)
		}
	}
	failed, e := s.QueueTeacherRefresh(ctx, p, "school", "full", "manual", "two")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimTeacherRefresh(ctx, p, "school"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteTeacherRefresh(ctx, p, failed.ID, c.TeacherRefreshCompletion{FailureReason: "request_failed"}); e != nil {
		t.Fatal(e)
	}
	retained, e := s.ReadUnmatchedTeacherSites(ctx, p, "school", "", "", 2)
	if e != nil || retained.Revision != page.Revision || retained.Total != 3 {
		t.Fatal("failure destroyed snapshot", retained, e)
	}
	status, e := s.TeacherRefreshStatus(ctx, p, "school")
	if e != nil || status.Latest.Status != "failed" || status.Counts.Eligible != 3 {
		t.Fatal(status, e)
	}
	denied := p
	denied.Schools = []string{"other"}
	if _, e = s.ReadUnmatchedTeacherSites(ctx, denied, "school", "", "", 20); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("cross school", e)
	}
	denied = p
	denied.CanReadTeacherSites = false
	denied.CanRefreshTeacherSites = false
	if _, e = s.TeacherRefreshStatus(ctx, denied, "school"); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked read", e)
	}
	if _, e = s.QueueTeacherRefresh(ctx, denied, "school", "full", "manual", "denied"); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked refresh", e)
	}
	repo := s.Repo.(*postgres.Repository)
	var audits int
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.audit WHERE audit_action LIKE 'catalog.teacher-sites.%' AND result='denied'`).Scan(&audits); e != nil || audits != 3 {
		t.Fatal("denied audit", audits, e)
	}
	migrated := c.TeacherUnmatchedSnapshot{SchoolID: "school", Revision: 2, ObservedAt: time.Now(), Items: []c.UnmatchedTeacherSite{}}
	if e = s.ImportTeacherUnmatched(ctx, p, migrated); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadUnmatchedTeacherSites(ctx, p, "school", "", page.Next, 2); !errors.Is(e, c.ErrConflict) {
		t.Fatal("old snapshot cursor accepted", e)
	}
}

func TestTeacherRefreshReadersDoNotWaitForWorkerPublication(t *testing.T) {
	s, p, snapshot := refreshFixture(t)
	ctx := context.Background()
	run, err := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", "seed-read")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTeacherRefresh(ctx, p, "school"); err != nil {
		t.Fatal(err)
	}
	completeRefresh(t, s, p, run, snapshot)
	if _, err = s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", "active-read"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTeacherRefresh(ctx, p, "school"); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	writer, err := repo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('catalog:teacher-refresh',0));SELECT pg_advisory_xact_lock(hashtextextended('catalog:school',0))`); err != nil {
		t.Fatal(err)
	}
	read, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	status, err := s.TeacherRefreshStatus(read, p, "school")
	if err != nil || status.Latest == nil || status.Latest.Status != "running" {
		t.Fatal("status blocked by worker", status, err)
	}
	page, err := s.ReadUnmatchedTeacherSites(read, p, "school", "", "", 20)
	if err != nil || page.Total != 3 {
		t.Fatal("snapshot blocked by worker", page, err)
	}
}

func TestTeacherUnmatchedEmptySnapshotIsIdempotent(t *testing.T) {
	s, p, snapshot := refreshFixture(t)
	ctx := context.Background()
	in := c.TeacherUnmatchedSnapshot{SchoolID: "school", Revision: snapshot.Revision, ObservedAt: snapshot.ObservedAt}
	for range 2 {
		if err := s.ImportTeacherUnmatched(ctx, p, in); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ReadUnmatchedTeacherSites(ctx, p, "school", "", "", 20)
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestTeacherRefreshAuditAtomicityAndHTTPBoundary(t *testing.T) {
	repo, migration := database(t)
	s := &c.Service{Repo: repo}
	p := c.Principal{Subject: "operator", Schools: []string{"school"}, CanImport: true, CanReadTeacherSites: true, CanRefreshTeacherSites: true}
	ctx := context.Background()
	if _, e := s.Import(ctx, p, "seed", c.Import{SchoolID: "school", SourceID: "synthetic", Revision: 1, FetchedAt: time.Now(), Objects: []c.Object{{ID: "school", Kind: "school", SchoolID: "school", Name: "School"}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := migration.Exec(`CREATE FUNCTION catalog.reject_refresh_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER reject_refresh_audit BEFORE INSERT ON catalog.audit FOR EACH ROW EXECUTE FUNCTION catalog.reject_refresh_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.QueueTeacherRefresh(ctx, p, "school", "incremental", "manual", "atomic"); e == nil {
		t.Fatal("audit failure accepted")
	}
	var jobs int
	if e := repo.DB.QueryRow(`SELECT count(*) FROM catalog.teacher_refresh_runs`).Scan(&jobs); e != nil || jobs != 0 {
		t.Fatal("audit failure left job", jobs, e)
	}
	if _, e := migration.Exec(`DROP TRIGGER reject_refresh_audit ON catalog.audit`); e != nil {
		t.Fatal(e)
	}
	current := p
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return current, nil }}.Routes()
	request := func(method, path, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "http")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d body=%s", method, path, w.Code, w.Body.String())
		}
	}
	path := "/api/v1/catalog/admin/schools/school/teacher-sites/refresh"
	current = c.Principal{}
	request("GET", path, "", 401)
	current = c.Principal{Subject: "member"}
	request("GET", path, "", 403)
	request("POST", path, `{"mode":"full"}`, 403)
	current = p
	request("POST", path, `{"mode":"full","staff_number":"synthetic"}`, 400)
	request("POST", path, `{"mode":"incremental"}`, 202)
	request("GET", path, "", 200)
	request("GET", "/api/v1/catalog/admin/schools/school/teacher-sites/unmatched?reason=unknown", "", 400)
}
