package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func teacherDirectoryFixture(t *testing.T) (*c.Service, c.Principal, c.TeacherSiteSnapshot) {
	t.Helper()
	s, p, in := fixture(t)
	p.Subject = "issuer:student"
	for i := range in.Objects {
		if in.Objects[i].Kind == "teacher" {
			in.Objects[i].DepartmentID = "dept"
		}
	}
	for _, id := range []string{"teacher3", "teacher4"} {
		in.Objects = append(in.Objects, c.Object{ID: id, Kind: "teacher", SchoolID: "school", Name: id, DepartmentID: "dept", Aliases: []string{}})
	}
	if _, err := s.Import(context.Background(), p, "seed", in); err != nil {
		t.Fatal(err)
	}
	s.Repo.(*postgres.Repository).RequireTeacherDirectory = true
	return s, p, c.TeacherSiteSnapshot{SchoolID: "school", SourceID: "official", Revision: 1, ObservedAt: time.Now().UTC(), Matches: []c.TeacherSiteMatch{
		{TeacherID: "teacher2", Name: "teacher2", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/synthetic2/zh_CN/index.htm"},
		{TeacherID: "teacher3", Name: "teacher3", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/synthetic3/en/index.htm"},
	}}
}

func TestTeacherDirectoryUnionFiltersEveryReadBeforePagination(t *testing.T) {
	s, p, snapshot := teacherDirectoryFixture(t)
	ctx := context.Background()
	before, err := s.List(ctx, c.Filter{Kind: "teacher", SchoolID: "school", Limit: 10})
	if err != nil || len(before.Items) != 0 {
		t.Fatal("missing markers must fail closed", before, err)
	}
	if result, err := s.RefreshTeachingDirectory(ctx, p, "school"); err != nil || result.Teaching != 2 || result.Eligible != 2 {
		t.Fatal("teaching evidence", result, err)
	}
	if result, err := s.ImportTeacherSites(ctx, p, snapshot); err != nil || result != (c.TeacherDirectoryResult{Teaching: 2, Official: 2, Both: 1, Eligible: 3}) {
		t.Fatal("union", result, err)
	}
	for _, query := range []string{"", "teacher"} {
		page, err := s.ReadList(ctx, p, c.Filter{Kind: "teacher", Query: query, IncludeInactive: true, Limit: 2})
		if err != nil || len(page.Items) != 2 || page.Next != "teacher2" {
			t.Fatal("first page", page, err)
		}
		page, err = s.ReadList(ctx, p, c.Filter{Kind: "teacher", Query: query, After: page.Next, IncludeInactive: true, Limit: 2})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "teacher3" || page.Next != "" {
			t.Fatal("next page", page, err)
		}
	}
	for _, id := range []string{"teacher", "teacher2", "teacher3"} {
		if _, err := s.Read(ctx, p, id); err != nil {
			t.Fatal("marked teacher missing", err)
		}
	}
	if _, err := s.Read(ctx, p, "teacher4"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("unmarked detail", err)
	}
	if _, err := s.ReadSummary(ctx, p, "teacher4"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("unmarked summary", err)
	}
	if _, err := s.ReadResolution(ctx, p, "teacher4"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("unmarked resolution", err)
	}
	lookup, err := s.ReadLookup(ctx, p, []string{"teacher3", "teacher4"})
	if err != nil || len(lookup.Items) != 1 || len(lookup.MissingIDs) != 1 || lookup.MissingIDs[0] != "teacher4" {
		t.Fatal("lookup", lookup, err)
	}
	current, err := s.DiscoveryCurrent(ctx, p, []string{"teacher", "teacher2", "teacher3", "teacher4"})
	if err != nil || len(current) != 3 {
		t.Fatal("stale search hits", current, err)
	}
	filtered, err := s.ReadList(ctx, p, c.Filter{Kind: "teacher", CourseID: "course", Limit: 10})
	if err != nil || len(filtered.Items) != 2 {
		t.Fatal("related teacher options", filtered, err)
	}
	summary, err := s.ReadSummary(ctx, p, "course")
	if err != nil || summary.TeacherCount != 2 {
		t.Fatal("teacher count", summary, err)
	}
	// An unmarked assignment added before the next completed refresh cannot leak
	// through the summary count or related teacher option query.
	_, err = s.Import(ctx, p, "new-relation", c.Import{SchoolID: "school", SourceID: "approved-source", Revision: 2, FetchedAt: time.Now(),
		Objects: []c.Object{{ID: "assignment4", Kind: "assignment", SchoolID: "school", Name: "assignment4", ClassID: "class1", TeacherID: "teacher4"}}})
	if err != nil {
		t.Fatal(err)
	}
	summary, err = s.ReadSummary(ctx, p, "course")
	if err != nil || summary.TeacherCount != 2 {
		t.Fatal("unmarked teacher included in count", summary, err)
	}
	if result, err := s.RefreshTeachingDirectory(ctx, p, "school"); err != nil || result.Eligible != 4 {
		t.Fatal("periodic teaching refresh", result, err)
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return c.Principal{}, nil }}.Routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/catalog/objects?kind=teacher", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous", w.Code)
	}
}

func TestTeacherSitesAtomicReplayRevocationAndChangedIdentity(t *testing.T) {
	s, p, snapshot := teacherDirectoryFixture(t)
	ctx := context.Background()
	if _, err := s.RefreshTeachingDirectory(ctx, p, "school"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ImportTeacherSites(ctx, p, snapshot); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent replay", err)
		}
	}
	db := s.Repo.(*postgres.Repository).DB
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM catalog.audit WHERE data->>'Type'='catalog.teacher-sites.refreshed.v1' OR data->>'type'='catalog.teacher-sites.refreshed.v1'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("one import audit", audits, err)
	}
	conflict := snapshot
	conflict.Matches = nil
	if _, err := s.ImportTeacherSites(ctx, p, conflict); !errors.Is(err, c.ErrConflict) {
		t.Fatal("same revision changed content", err)
	}
	bad := snapshot
	bad.Revision++
	bad.Matches = []c.TeacherSiteMatch{{TeacherID: "teacher3", Name: "wrong", DepartmentID: "dept", URL: snapshot.Matches[0].URL}}
	if _, err := s.ImportTeacherSites(ctx, p, bad); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale matching facts", err)
	}
	if _, err := s.Read(ctx, p, "teacher3"); err != nil {
		t.Fatal("failed refresh removed old evidence", err)
	}
	teacher, _ := s.Get(ctx, "teacher3")
	name := "changed"
	if _, err := s.Correct(ctx, p, "teacher3", "rename", c.Correction{Revision: teacher.Revision, Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, p, "teacher3"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("old name match still visible", err)
	}
	if current, err := s.DiscoveryCurrent(ctx, p, []string{"teacher3"}); err != nil || len(current) != 0 {
		t.Fatal("renamed stale search result", current, err)
	}
	conflict.Revision = 3
	if result, err := s.ImportTeacherSites(ctx, p, conflict); err != nil || result.Eligible != 2 || result.Official != 0 {
		t.Fatal("official revocation must preserve teaching", result, err)
	}
	if _, err := s.ImportTeacherSites(ctx, p, snapshot); !errors.Is(err, c.ErrConflict) {
		t.Fatal("old revision revived evidence", err)
	}
	if _, err := s.ImportTeacherSites(ctx, c.Principal{Subject: "outsider", Schools: []string{"elsewhere"}, CanImport: true}, snapshot); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("cross-school write", err)
	}
	if _, err := s.TeacherCandidates(ctx, c.Principal{Subject: "reader"}, "school", "", 10); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("private candidate access", err)
	}
}
