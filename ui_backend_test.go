package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

type uiMetrics struct {
	rows []c.DirectoryMetric
	err  error
}

func (m uiMetrics) Metrics(context.Context, c.Principal, string, string, time.Time, time.Time) ([]c.DirectoryMetric, error) {
	return m.rows, m.err
}

type recentCounts struct{ failed bool }

func (r recentCounts) CountRecentResources(_ context.Context, _ c.Principal, _ string, ids []string) (map[string]int64, error) {
	if r.failed {
		return nil, c.ErrUnavailable
	}
	out := map[string]int64{}
	for _, id := range ids {
		out[id] = 2
	}
	return out, nil
}

func TestUIRecentCoursesOwnerRetentionConcurrencyAndClear(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }
	input.FetchedAt = now
	for i := 0; i < 25; i++ {
		input.Objects = append(input.Objects, c.Object{ID: fmt.Sprintf("recent-%02d", i), Kind: "course", SchoolID: "school", Name: fmt.Sprint(i)})
	}
	if _, err := s.Import(ctx, admin, "ui-seed", input); err != nil {
		t.Fatal(err)
	}
	p := c.Principal{Subject: "issuer:student"}
	for i := 0; i < 25; i++ {
		now = now.Add(time.Minute)
		if err := s.RecordRecentCourse(ctx, p, fmt.Sprintf("recent-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	s.RecentResources = recentCounts{}
	page, err := s.MyRecentCourses(ctx, p)
	if err != nil || len(page.Items) != 20 || page.Items[0].Course.ID != "recent-24" || page.Items[19].Course.ID != "recent-05" || page.ResourcesState != "available" || page.Items[0].ResourceCount == nil || *page.Items[0].ResourceCount != 2 {
		t.Fatalf("recent: %+v %v", page, err)
	}
	other, err := s.MyRecentCourses(ctx, c.Principal{Subject: "issuer:teacher"})
	if err != nil || len(other.Items) != 0 {
		t.Fatal("another owner read visits", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			if err := s.RecordRecentCourse(ctx, p, "recent-00"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	page, err = s.MyRecentCourses(ctx, p)
	if err != nil || len(page.Items) != 20 {
		t.Fatal("concurrent limit", err, len(page.Items))
	}
	count := 0
	for _, item := range page.Items {
		if item.Course.ID == "recent-00" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("repeat visit duplicated", count)
	}
	s.RecentResources = recentCounts{failed: true}
	page, err = s.MyRecentCourses(ctx, p)
	if err != nil || page.ResourcesState != "unavailable" || page.Items[0].ResourceCount != nil {
		t.Fatal("failed count became zero", err)
	}
	if err := s.RecordRecentCourse(ctx, c.Principal{}, "course"); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if err := s.RecordRecentCourse(ctx, p, "teacher"); !errors.Is(err, c.ErrInput) {
		t.Fatal("teacher history was stored", err)
	}
	now = now.Add(90 * 24 * time.Hour)
	page, err = s.MyRecentCourses(ctx, p)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("expiry boundary", err, len(page.Items))
	}
	removed, err := s.PruneRecentCourses(ctx, 3)
	if err != nil || removed != 3 {
		t.Fatal("bounded expiry", removed, err)
	}
	if err := s.ClearRecentCourses(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearRecentCourses(ctx, p); err != nil {
		t.Fatal("clear replay", err)
	}
}

func TestUIDirectoryFacetsSortingVisibilityAndPagination(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	input.Objects = append(input.Objects, c.Object{ID: "another", Kind: "course", SchoolID: "school", Name: "A course", DepartmentID: "dept", CategoryID: "seminar"}, c.Object{ID: "unmarked", Kind: "teacher", SchoolID: "school", Name: "Private directory candidate"})
	if _, err := s.Import(ctx, admin, "ui-directory-seed", input); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	repo.RequireTeacherDirectory = true
	if _, err := s.RefreshTeachingDirectory(ctx, admin, "school"); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{rows: []c.DirectoryMetric{{ID: "course", ReviewCount: 3, RecentReviews: 2, Favorites: 1, Recommendation: "recommended"}, {ID: "another", ReviewCount: 5, RecentReviews: 0, Favorites: 0, Recommendation: "strongly_recommended"}, {ID: "outside-school", ReviewCount: 100, Recommendation: "strongly_recommended"}}}
	p := c.Principal{Subject: "issuer:student"}
	q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 1}, Sort: "hot"}
	first, err := s.ReadDirectory(ctx, p, q)
	if err != nil || first.Total != 2 || first.FacetTotal != 2 || len(first.Items) != 1 || first.Items[0].Object.ID != "course" || first.Items[0].Heat != 13 || first.Next == "" {
		t.Fatalf("hot: %+v %v", first, err)
	}
	if len(first.CategoryFacets) != 1 || first.CategoryFacets[0].Count != 2 || first.CategoryFacets[0].ReviewCount != 8 || len(first.DepartmentFacets) != 1 || first.DepartmentFacets[0].Count != 2 || first.DepartmentFacets[0].ReviewCount != 8 {
		t.Fatalf("facets: %+v", first)
	}
	q.After = first.Next
	second, err := s.ReadDirectory(ctx, p, q)
	if err != nil || len(second.Items) != 1 || second.Items[0].Object.ID != "another" || second.Next != "" {
		t.Fatalf("pagination: %+v %v", second, err)
	}
	q.Sort = "name"
	if _, err := s.ReadDirectory(ctx, p, q); !errors.Is(err, c.ErrInput) {
		t.Fatal("cursor ignored changed sort", err)
	}
	q.After = ""
	for _, sort := range []string{"name", "recommend", "reviews"} {
		q.Sort = sort
		v, err := s.ReadDirectory(ctx, p, q)
		if err != nil || v.Items[0].Object.ID != "another" {
			t.Fatalf("%s: %+v %v", sort, v, err)
		}
	}
	q.Limit = 20
	for _, category := range []string{"", "seminar", "missing"} {
		q.CategoryID = category
		page, err := s.ReadDirectory(ctx, p, q)
		if err != nil {
			t.Fatal(err)
		}
		old, err := s.ReadList(ctx, p, q.Filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != int64(len(old.Items)) || len(page.Items) != len(old.Items) || page.CategoryFacets[0].Count != 2 || page.CategoryFacets[0].ReviewCount != 8 {
			t.Fatal("count/list/filter mismatch", page, old)
		}
	}
	q = c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 20}, Sort: "name"}
	page, err := s.ReadDirectory(ctx, p, q)
	if err != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("marked teachers: %+v %v", page, err)
	}
	for _, item := range page.Items {
		if item.Object.ID == "unmarked" {
			t.Fatal("unmarked teacher exposed")
		}
	}
	repo.RequireTeacherDirectory = false
	if _, err := s.ReadDirectory(ctx, p, q); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("T2 enforcement missing but teacher directory opened", err)
	}
	if _, err := s.ReadDirectory(ctx, c.Principal{}, q); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal(err)
	}
	q.SchoolID = "other"
	if _, err := s.ReadDirectory(ctx, p, q); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("cross school allowed", err)
	}
}

func TestUIOverviewHTTPRejectsUnownedAndUnavailableSources(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, admin, "http-ui", input); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{}
	h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
		if req.Header.Get("Authorization") == "Bearer student" {
			return c.Principal{Subject: "issuer:student"}, nil
		}
		if req.Header.Get("Authorization") == "Bearer teacher" {
			return c.Principal{Subject: "issuer:teacher"}, nil
		}
		return c.Principal{}, nil
	}}.Routes()
	call := func(method, path, token, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	for _, path := range []string{"/api/v1/catalog/directory?kind=course", "/api/v1/catalog/me/recent-courses", "/api/v1/catalog/me/next-class-day"} {
		call("GET", path, "", "", 401)
	}
	call("PUT", "/api/v1/catalog/me/recent-courses/course", "Bearer student", "{\"owner\":\"issuer:teacher\"}", 400)
	call("PUT", "/api/v1/catalog/me/recent-courses/course", "Bearer student", "", 200)
	call("GET", "/api/v1/catalog/me/recent-courses?owner=issuer:student", "Bearer teacher", "", 400)
	other := call("GET", "/api/v1/catalog/me/recent-courses", "Bearer teacher", "", 200)
	var recent c.RecentCourses
	if json.Unmarshal(other.Body.Bytes(), &recent) != nil || len(recent.Items) != 0 {
		t.Fatal("forged owner exposed history")
	}
	v := call("GET", "/api/v1/catalog/me/next-class-day", "Bearer student", "", 200)
	var day c.NextClassDay
	if json.Unmarshal(v.Body.Bytes(), &day) != nil || day.State != "unavailable" || day.Date != nil || len(day.Items) != 0 {
		t.Fatal("unauthorized stored enrollment became next class", v.Body.String())
	}
	call("GET", "/api/v1/catalog/directory?kind=course&limit=101", "Bearer student", "", 400)
	call("GET", "/api/v1/catalog/directory?kind=course&sort=unknown", "Bearer student", "", 400)
	call("GET", "/api/v1/catalog/directory?kind=course&school_id=other", "Bearer student", "", 403)
	s.ReadAccess = &readAccess{revoked: true}
	call("GET", "/api/v1/catalog/me/recent-courses", "Bearer student", "", 403)
	call("DELETE", "/api/v1/catalog/me/recent-courses", "Bearer student", "", 200)
	call("DELETE", "/api/v1/catalog/me/recent-courses", "", "", 401)
}

func TestUIDirectorySameClassFacetCounts(t *testing.T) {
	s, p, input := fixture(t)
	ctx := context.Background()
	input.Objects = append(input.Objects, c.Object{ID: "other-dept", Kind: "department", SchoolID: "school", Name: "Other"}, c.Object{ID: "other-category", Kind: "category", SchoolID: "school", Name: "Other"}, c.Object{ID: "other-class", Kind: "class", SchoolID: "school", Name: "Other", TermCourseID: "offering1", DepartmentID: "other-dept", CategoryID: "other-category"})
	if _, err := s.Import(ctx, p, "same-class-ui", input); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{}
	q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", DepartmentID: "other-dept", CategoryID: "seminar", Limit: 20}, Sort: "name"}
	got, err := s.ReadDirectory(ctx, c.Principal{Subject: "issuer:student"}, q)
	if err != nil || got.Total != 0 {
		t.Fatal("classification crossed classes", got, err)
	}
	index := slices.IndexFunc(got.CategoryFacets, func(f c.DirectoryFacet) bool { return f.ID == "seminar" })
	if index < 0 || got.CategoryFacets[index].Count != 0 {
		t.Fatal("facet crossed classes", got)
	}
}
