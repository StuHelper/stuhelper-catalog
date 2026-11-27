package catalog_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func httpHandlerForTest(s *c.Service, principal func() c.Principal) http.Handler {
	return httpapi.Handler{
		Service:      s,
		Authenticate: func(*http.Request) (c.Principal, error) { return principal(), nil },
	}.Routes()
}

func serveJSONRequest(h http.Handler, method, path, idempotencyKey, body string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func projectionFixture(t *testing.T) (*c.Service, c.Principal, *sql.DB, c.ProjectionReport) {
	t.Helper()
	repo, admin := database(t)
	s := &c.Service{Repo: repo, SchoolDataAccess: fixtureSchoolData{}, ReadAccess: &readAccess{}, DirectoryMetrics: uiMetrics{}}
	p := c.Principal{Subject: "issuer:student", Schools: []string{"school"}, CanImport: true, CanMaintain: true}
	objects := []c.Object{{ID: "school", Kind: "school", Name: "Synthetic school"}, {ID: "dept", Kind: "department", Name: "Department"}, {ID: "unused", Kind: "category", Name: "Empty"},
		{ID: "category", Kind: "category", Name: "Visible category"}, {ID: "nature", Kind: "nature", Name: "Nature"},
		{ID: "course", Kind: "course", Name: "Current", DepartmentID: "dept"}, {ID: "old", Kind: "course", Name: "Old", DepartmentID: "dept", CategoryID: "unused"},
		{ID: "invalid", Kind: "course", Name: "Invalid", DepartmentID: "dept"},
		{ID: "teacher", Kind: "teacher", Name: "Teacher", DepartmentID: "dept"}, {ID: "legacy", Kind: "teacher", Name: "Legacy", DepartmentID: "dept"},
		{ID: "official", Kind: "teacher", Name: "Official", DepartmentID: "dept"},
		{ID: "pre-spring", Kind: "term", Name: "Spring 2023"}, {ID: "a-old", Kind: "term", Name: "Unreliable display 2099 autumn"}, {ID: "z-boundary", Kind: "term", Name: "Cutoff"},
		{ID: "z-spring", Kind: "term", Name: "Spring"}, {ID: "m-summer", Kind: "term", Name: "Summer"}, {ID: "a-autumn", Kind: "term", Name: "Autumn"},
		{ID: "unknown", Kind: "term", Name: "2028 Autumn guess"}, {ID: "empty-term", Kind: "term", Name: "No offerings"},
		{ID: "offering", Kind: "term_course", Name: "Current offering", CourseID: "course", TermID: "z-boundary"},
		{ID: "old-offering", Kind: "term_course", Name: "Old offering", CourseID: "old", TermID: "a-old"},
		{ID: "bad-offering", Kind: "term_course", Name: "Unknown offering", CourseID: "invalid", TermID: "unknown"},
		{ID: "spring-offering", Kind: "term_course", Name: "Spring offering", CourseID: "course", TermID: "z-spring"},
		{ID: "summer-offering", Kind: "term_course", Name: "Summer offering", CourseID: "course", TermID: "m-summer"},
		{ID: "autumn-offering", Kind: "term_course", Name: "Autumn offering", CourseID: "course", TermID: "a-autumn"},
		{ID: "class", Kind: "class", Name: "Current class", TermCourseID: "offering", CategoryID: "category", NatureID: "nature"},
		{ID: "old-class", Kind: "class", Name: "Old class", TermCourseID: "old-offering"},
		{ID: "assignment", Kind: "assignment", Name: "Current teaching", ClassID: "class", TeacherID: "teacher"},
		{ID: "old-assignment", Kind: "assignment", Name: "Old teaching", ClassID: "old-class", TeacherID: "legacy"}}
	for i := range objects {
		objects[i].SchoolID = "school"
		objects[i].Aliases = []string{}
	}
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed", c.Import{SchoolID: "school", SourceID: "source", Revision: 1, FetchedAt: time.Now(), Objects: objects}); err != nil {
		t.Fatal(err)
	}
	terms := []c.TermOrder{{ID: "pre-spring", Year: 2023, Season: "spring"}, {ID: "a-old", Year: 2023, Season: "summer"}, {ID: "z-boundary", Year: 2023, Season: "autumn"}, {ID: "z-spring", Year: 2024, Season: "spring"},
		{ID: "m-summer", Year: 2024, Season: "summer"}, {ID: "a-autumn", Year: 2024, Season: "autumn"}, {ID: "unknown", Reason: "invalid_term_code"}, {ID: "empty-term", Year: 2025, Season: "autumn"}}
	if err := s.SaveTermOrders(ctx, p, "school", terms); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.TeacherCandidates(ctx, p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{SchoolID: "school", SourceID: "official", Revision: 1, ObservedAt: time.Now(), CandidateFingerprint: c.TeacherCandidateFingerprint(candidates),
		Matches: []c.TeacherSiteMatch{{TeacherID: "official", Name: "Official", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/synthetic/en/index.htm"}}}); err != nil {
		t.Fatal(err)
	}
	report, err := s.RebuildDirectory(ctx, p, "school", nil)
	if err != nil {
		t.Fatal(err)
	}
	repo.RequireTeacherDirectory = true
	repo.RequireDirectoryProjection = true
	return s, p, admin, report
}

func TestDirectoryProjectionCutoffOrderingCountsAndAbsentOldCourses(t *testing.T) {
	s, p, admin, report := projectionFixture(t)
	ctx := context.Background()
	if report.Courses != 1 || report.Teachers != 2 || report.InvalidTerms != 1 || report.Marks != (c.TeacherDirectoryResult{Teaching: 1, Official: 1, Both: 0, Eligible: 2}) {
		t.Fatalf("report: %+v", report)
	}
	ids := []string{}
	after := ""
	for {
		page, err := s.ReadList(ctx, p, c.Filter{Kind: "term", Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range page.Items {
			ids = append(ids, o.ID)
			if o.TermOrderKey < 20233 {
				t.Fatal(o)
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !slices.Equal(ids, []string{"a-autumn", "m-summer", "z-spring", "z-boundary"}) {
		t.Fatal("chronological order", ids)
	}
	for _, kind := range []string{"course", "teacher"} {
		options, err := s.ReadFilterOptions(ctx, p, kind, "")
		if err != nil {
			t.Fatal(err)
		}
		directory, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: kind, Limit: 100}, Sort: "name"})
		if err != nil || options.Total != directory.Total || int64(len(directory.Items)) != options.Total {
			t.Fatal(options, directory, err)
		}
		for group, values := range map[string][]c.FilterOption{"department": options.Departments, "category": options.Categories, "nature": options.Natures} {
			for _, v := range values {
				f := c.Filter{Kind: kind, Limit: 100}
				switch group {
				case "department":
					f.DepartmentID = v.ID
				case "category":
					f.CategoryID = v.ID
				case "nature":
					f.NatureID = v.ID
				}
				page, e := s.ReadList(ctx, p, f)
				if e != nil || v.Count == 0 || v.ID == "unused" || int64(len(page.Items)) != v.Count {
					t.Fatal("count mismatch", v, page, e)
				}
			}
		}
	}
	for _, id := range []string{"old", "invalid"} {
		if _, err := s.Read(ctx, p, id); !errors.Is(err, c.ErrNotFound) {
			t.Fatal("old course detail must be absent", id, err)
		}
		if _, err := s.ReadSummary(ctx, p, id); !errors.Is(err, c.ErrNotFound) {
			t.Fatal("old course summary must be absent", id, err)
		}
		page, err := s.ReadList(ctx, p, c.Filter{Kind: "course", IDs: []string{id}, Limit: 100})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("old course in list", page, err)
		}
	}
	var oldRows int
	if err := admin.QueryRow(`SELECT count(*) FROM catalog.projection_objects WHERE version=$1 AND kind='course' AND NOT visible`, report.Version).Scan(&oldRows); err != nil || oldRows != 0 {
		t.Fatal("projection retained invisible courses", oldRows, err)
	}
	lookup, err := s.ReadLookup(ctx, p, []string{"old", "invalid"})
	if err != nil || len(lookup.Items) != 0 || len(lookup.MissingIDs) != 2 {
		t.Fatal("old course in public lookup", lookup, err)
	}
	for _, sort := range []string{"hot", "name", "recommend", "reviews"} {
		page, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 100}, Sort: sort})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Object.ID != "course" {
			t.Fatal("old course in directory sort", sort, page, err)
		}
	}
	summary, err := s.ReadSummary(ctx, p, "course")
	if err != nil || summary.TeacherCount != 1 || summary.CourseCount != 1 {
		t.Fatal("summary", summary, err)
	}
	internalSummary, err := s.Summary(ctx, "course")
	if err != nil || internalSummary.OfferingCount != 4 {
		t.Fatal("trusted offering count", internalSummary, err)
	}
	page, err := s.ReadList(ctx, p, c.Filter{Kind: "course", TeacherID: "teacher", CategoryID: "category", NatureID: "nature", Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	page, err = s.ReadList(ctx, p, c.Filter{Kind: "course", TeacherID: "legacy", Limit: 100})
	if !errors.Is(err, c.ErrNotFound) || len(page.Items) != 0 {
		t.Fatal("old teaching", page, err)
	}
	if _, err = s.Read(ctx, p, "legacy"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("old teacher detail", err)
	}
	if yes, err := s.HasTaught(ctx, []string{"legacy"}, "old"); err != nil || yes {
		t.Fatal("old course retained in teaching projection", yes, err)
	}
	docs, err := s.DiscoveryCurrent(ctx, p, []string{"course", "old", "teacher", "legacy", "official"})
	if err != nil || len(docs) != 3 {
		t.Fatal("search visibility", docs, err)
	}
	if _, err = s.ReadFilterOptions(ctx, c.Principal{}, "course", ""); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = s.ReadFilterOptions(ctx, p, "course", "another-school"); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	s.ReadAccess = &readAccess{revoked: true}
	if _, err = s.ReadList(ctx, p, c.Filter{Kind: "course", Limit: 100}); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("revoked authority", err)
	}
}

func TestReviewOfferingTermsUseCourseAndCutoff(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	ctx := context.Background()
	now := time.Now()
	objects := []c.Object{
		{ID: "same-course-spring2023", Kind: "term_course", SchoolID: "school", Name: "Before cutoff spring", CourseID: "course", TermID: "pre-spring"},
		{ID: "same-course-old-offering", Kind: "term_course", SchoolID: "school", Name: "Before cutoff", CourseID: "course", TermID: "a-old", Aliases: []string{}},
		{ID: "same-course-unknown-offering", Kind: "term_course", SchoolID: "school", Name: "Unknown term", CourseID: "course", TermID: "unknown", Aliases: []string{}},
		{ID: "historical-class", Kind: "class", SchoolID: "school", Name: "Historical class", TermCourseID: "same-course-old-offering"},
		{ID: "historical-teaching", Kind: "assignment", SchoolID: "school", Name: "Historical teaching", ClassID: "historical-class", TeacherID: "official"},
		{ID: "other-course-offering", Kind: "term_course", SchoolID: "school", Name: "Only another course", CourseID: "old", TermID: "empty-term", Aliases: []string{}},
	}
	if _, err := s.Import(ctx, p, "review-offerings", c.Import{SchoolID: "school", SourceID: "synthetic-review", Revision: 1, FetchedAt: now, Objects: objects}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RebuildDirectory(ctx, p, "school", nil); err != nil {
		t.Fatal(err)
	}
	terms, err := s.CourseOfferingTerms(ctx, "course")
	var got []string
	for _, term := range terms {
		if term.TermOrderKey < 20233 || term.Name == "" {
			t.Fatalf("unusable term candidate: %+v", term)
		}
		got = append(got, term.ID)
	}
	if err != nil || !slices.Equal(got, []string{"a-autumn", "m-summer", "z-spring", "z-boundary"}) {
		t.Fatal(got, err)
	}
	page, err := s.ReadList(ctx, p, c.Filter{Kind: "term", CourseID: "course", Limit: 100})
	if err != nil || !slices.EqualFunc(page.Items, terms, func(a, b c.Object) bool { return a.ID == b.ID }) {
		t.Fatal(page, err)
	}
	for _, term := range []string{"pre-spring", "a-old"} {
		if _, err := s.ReviewTerm(ctx, term); !errors.Is(err, c.ErrTermBeforeStart) {
			t.Fatalf("pre-floor term %s returned %v", term, err)
		}
	}
	for _, read := range []func(context.Context, c.Principal, c.Filter) (c.Page, error){s.ReadList, s.AdminList} {
		filtered, err := read(ctx, p, c.Filter{Kind: "teacher", SchoolID: "school", CourseID: "course", Limit: 100})
		if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != "teacher" {
			t.Fatal("course teacher filter includes historical-only teaching", filtered, err)
		}
	}
	if _, err := s.CourseTermTeachers(ctx, "course", "empty-term"); !errors.Is(err, c.ErrInput) {
		t.Fatal("another course's term was accepted", err)
	}
	teachers, err := s.CourseTermTeachers(ctx, "course", "z-boundary")
	if err != nil || !slices.Equal(teachers, []string{"teacher"}) {
		t.Fatal(teachers, err)
	}
	teachers, err = s.CourseTermTeachers(ctx, "course", "m-summer")
	if err != nil || len(teachers) != 0 {
		t.Fatal("an offering can have no teacher", teachers, err)
	}
}

func TestDirectoryProjectionRebuildFailureKeepsVersionAndRecordsReason(t *testing.T) {
	s, p, admin, first := projectionFixture(t)
	ctx := context.Background()
	if _, err := admin.Exec(`CREATE FUNCTION catalog.reject_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$;
 CREATE TRIGGER reject_projection BEFORE INSERT ON catalog.projection_options FOR EACH ROW EXECUTE FUNCTION catalog.reject_projection()`); err != nil {
		t.Fatal(err)
	}
	policy := c.DirectoryPolicy{CutoffYear: 2024, CutoffSeason: "spring"}
	if _, err := s.RebuildDirectory(ctx, p, "school", &policy); err == nil {
		t.Fatal("expected injected failure")
	}
	var version int64
	var reason string
	if err := admin.QueryRow(`SELECT version FROM catalog.projection_heads WHERE school_id='school'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != first.Version {
		t.Fatal("failed generation published", version)
	}
	if err := admin.QueryRow(`SELECT reason FROM catalog.projection_failures WHERE school_id='school'`).Scan(&reason); err != nil || reason != "build_failed" {
		t.Fatal(reason, err)
	}
	if _, err := s.Read(ctx, p, "teacher"); err != nil {
		t.Fatal("old version unreadable", err)
	}
	if _, err := admin.Exec(`DROP TRIGGER reject_projection ON catalog.projection_options`); err != nil {
		t.Fatal(err)
	}
	report, err := s.RebuildDirectory(ctx, p, "school", &policy)
	if err != nil || report.Marks.Teaching != 0 || report.Marks.Official != 1 || report.Version <= first.Version {
		t.Fatal(report, err)
	}
	if _, err := s.Read(ctx, p, "teacher"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("new policy failed", err)
	}
	if _, err := s.Read(ctx, p, "official"); err != nil {
		t.Fatal("official unaffected", err)
	}
}

func TestDirectoryProjectionAutoRebuildAndCompleteImportBoundary(t *testing.T) {
	s, p, _, first := projectionFixture(t)
	ctx := context.Background()
	newObjects := []c.Object{{ID: "new-course", Kind: "course", SchoolID: "school", Name: "New course", CategoryID: "unused"}, {ID: "new-offering", Kind: "term_course", SchoolID: "school", Name: "New offering", CourseID: "new-course", TermID: "z-boundary"}}
	if _, err := s.Import(c.DeferDirectoryRebuild(ctx), p, "batch-page", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: newObjects}); err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadFilterOptions(ctx, p, "course", "")
	if err != nil || before.Total != 1 {
		t.Fatal("partial import published", before, err)
	}
	report, err := s.RebuildDirectory(ctx, p, "school", nil)
	if err != nil || report.Version <= first.Version {
		t.Fatal(report, err)
	}
	after, err := s.ReadFilterOptions(ctx, p, "course", "")
	if err != nil || after.Total != 2 || len(after.Categories) != 2 {
		t.Fatal("new category missing", after, err)
	}
	object, err := s.Get(ctx, "new-course")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetState(ctx, p, "new-course", "hide-new", c.ObjectState{Revision: object.Revision, Inactive: true, Reason: "Synthetic hide"}); err != nil {
		t.Fatal(err)
	}
	after, err = s.ReadFilterOptions(ctx, p, "course", "")
	if err != nil || after.Total != 1 || len(after.Categories) != 1 {
		t.Fatal("state did not rebuild", after, err)
	}
}

func TestDirectoryProjectionConcurrentReadersAndSearchSnapshot(t *testing.T) {
	s, p, admin, _ := projectionFixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	page, err := repo.DiscoverySnapshot(ctx, "", 1)
	if err != nil || page.Next == "" {
		t.Fatal(page, err)
	}
	cursor := page.Next
	directory, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 1}, Sort: "name"})
	if err != nil || directory.Next == "" {
		t.Fatal(directory, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				out, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 100}, Sort: "name"})
				if err != nil || (out.Total != 1 && out.Total != 2) || int64(len(out.Items)) != out.Total {
					errs <- fmt.Errorf("incomplete generation: %d %v", out.Total, err)
					return
				}
			}
		}()
	}
	if _, err := admin.Exec(`CREATE FUNCTION catalog.slow_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.02); RETURN NEW; END $$;
 CREATE TRIGGER slow_projection BEFORE INSERT ON catalog.projection_options FOR EACH ROW EXECUTE FUNCTION catalog.slow_projection()`); err != nil {
		t.Fatal(err)
	}
	policy := c.DirectoryPolicy{CutoffYear: 2024, CutoffSeason: "spring"}
	if _, err = s.RebuildDirectory(ctx, p, "school", &policy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 1, After: directory.Next}, Sort: "name"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("old directory cursor mixed generations", err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if _, err = repo.DiscoverySnapshot(ctx, cursor, 1); err != nil {
		t.Fatal("previous complete snapshot missing", err)
	}
	if _, err = s.RebuildDirectory(ctx, p, "school", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.DiscoverySnapshot(ctx, cursor, 1); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("pruned cursor accepted", err)
	}
}

func TestDirectoryProjectionFailedSourceImportFencesPublicationUntilRetry(t *testing.T) {
	s, p, _, first := projectionFixture(t)
	ctx := context.Background()
	partial := c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: []c.Object{{ID: "pending", Kind: "course", SchoolID: "school", Name: "Pending"}}}
	interrupted := errors.New("synthetic interrupted source")
	if _, err := s.RunDirectoryImport(ctx, p, "school", "source/batch-two", func(call context.Context) error {
		if _, err := s.Import(call, p, "partial-page", partial); err != nil {
			return err
		}
		if _, err := s.RunDirectoryImport(ctx, p, "school", "source/batch-two", func(context.Context) error { return nil }); !errors.Is(err, c.ErrConflict) {
			return fmt.Errorf("concurrent importer accepted: %v", err)
		}
		return interrupted
	}); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	before, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil || before.Version != first.Version {
		t.Fatal("partial source published", before, err)
	}
	if _, err = s.RebuildDirectory(ctx, p, "school", nil); !errors.Is(err, c.ErrConflict) {
		t.Fatal("config published partial source", err)
	}
	object, err := s.Get(ctx, "course")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetState(ctx, p, "course", "concurrent-state", c.ObjectState{Revision: object.Revision, Inactive: true, Reason: "Synthetic concurrent mutation"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("mutation published partial source", err)
	}
	if _, err = s.Read(ctx, p, "pending"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("partial object escaped", err)
	}
	result, err := s.RunDirectoryImport(ctx, p, "school", "source/batch-two", func(call context.Context) error {
		if _, err := s.Import(call, p, "partial-page", partial); err != nil {
			return err
		}
		_, err := s.Import(call, p, "last-page", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: []c.Object{{ID: "pending-offering", Kind: "term_course", SchoolID: "school", Name: "Pending offering", CourseID: "pending", TermID: "z-boundary"}}})
		return err
	})
	if err != nil || result.Courses != 2 || result.Version <= first.Version {
		t.Fatal("retry did not atomically publish", result, err)
	}
	object, err = s.Read(ctx, p, "pending")
	if err != nil || object.ID != "pending" {
		t.Fatal(object, err)
	}
}

func TestProjectionMaintenanceRebuildUsesInjectedClock(t *testing.T) {
	s, p, _, initial := projectionFixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	repo.RequireDirectoryProjection = true
	repo.RequireTeacherDirectory = true

	fixedClock := initial.BuiltAt.Add(2 * time.Hour).Truncate(time.Second)
	s.Clock = func() time.Time { return fixedClock }

	updatedName := "Clock Controlled Course"
	course, err := s.AdminRead(ctx, p, "course")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Correct(ctx, p, "course", "clock-correct", c.Correction{Revision: course.Revision, Name: &updatedName}); err != nil {
		t.Fatal(err)
	}

	report, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil {
		t.Fatal(err)
	}
	if !report.BuiltAt.Equal(fixedClock) {
		t.Fatalf("rebuildActive ignored Service.Clock: got BuiltAt=%v want=%v", report.BuiltAt, fixedClock)
	}
	readCourse, err := s.Read(ctx, p, "course")
	if err != nil || readCourse.Name != updatedName {
		t.Fatalf("projected Read did not reflect corrected name: %+v %v", readCourse, err)
	}

	// Verify ImportTeacherSites also forwards s.Clock into RebuildPublishedDirectory.
	nextClock := fixedClock.Add(30 * time.Minute)
	s.Clock = func() time.Time { return nextClock }
	candidates, err := s.TeacherCandidates(ctx, p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{
		CandidateFingerprint: c.TeacherCandidateFingerprint(candidates),
		SchoolID:             "school", SourceID: "official", Revision: 2, ObservedAt: nextClock,
		Matches: []c.TeacherSiteMatch{{TeacherID: "official", Name: "Official", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/official/index.htm"}},
	}); err != nil {
		t.Fatal(err)
	}
	report2, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil || !report2.BuiltAt.Equal(nextClock) {
		t.Fatalf("ImportTeacherSites rebuild ignored Service.Clock: got=%+v want=%v err=%v", report2, nextClock, err)
	}
}

func TestAdminDirectoryProjectionHTTPResetFieldsAndDebouncedWorker(t *testing.T) {
	s, p, _, first := projectionFixture(t)
	ctx := context.Background()

	// 1. reset_fields restores imported values and clears Manual entries.
	manualName := "人工临时名称"
	manualCredits := 4.5
	updated, err := s.Correct(ctx, p, "course", "set-manual", c.Correction{
		Revision: 1,
		Name:     &manualName,
		Credits:  &manualCredits,
	})
	if err != nil || updated.Name != manualName || updated.Credits == nil || *updated.Credits != 4.5 {
		t.Fatalf("set manual: %+v %v", updated, err)
	}
	// Reject setting and resetting the same field simultaneously.
	if _, err := s.Correct(ctx, p, "course", "bad-reset", c.Correction{
		Revision:    updated.Revision,
		Name:        &manualName,
		ResetFields: []string{"name"},
	}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("expected ErrInput on simultaneous set+reset, got %v", err)
	}
	restored, err := s.Correct(ctx, p, "course", "reset-manual", c.Correction{
		Revision:    updated.Revision,
		ResetFields: []string{"name", "credits"},
	})
	if err != nil || restored.Name != "Current" || restored.Credits != nil {
		t.Fatalf("reset manual failed: %+v %v", restored, err)
	}

	// 2. AsyncMaintenanceRebuild coalesces rapid mutations via RunProjectionWorker.
	s.AsyncMaintenanceRebuild = true
	beforeReport, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil {
		t.Fatal(err)
	}
	n1, n2 := "Burst 1", "Burst 2"
	u1, err := s.Correct(ctx, p, "course", "burst-1", c.Correction{Revision: restored.Revision, Name: &n1})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.Correct(ctx, p, "course", "burst-2", c.Correction{Revision: u1.Revision, Name: &n2})
	if err != nil {
		t.Fatal(err)
	}
	// Synchronous transaction did not rebuild yet; pending_rebuild is true.
	status, err := s.AdminDirectoryProjection(ctx, p, "school")
	if err != nil || status.Published == nil || status.Published.Version != beforeReport.Version || !status.PendingRebuild || status.SourceRevisionLag < 1 {
		t.Fatalf("expected pending coalesced rebuild: %+v %v", status, err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- s.RunProjectionWorker(workerCtx, p, "school", 40*time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, err := s.AdminDirectoryProjection(ctx, p, "school")
		if err != nil {
			stopWorker()
			t.Fatal(err)
		}
		if !cur.PendingRebuild && cur.Published != nil && cur.Published.Version > beforeReport.Version {
			break
		}
		if time.Now().After(deadline) {
			stopWorker()
			t.Fatalf("debounced worker did not publish: %+v", cur)
		}
		time.Sleep(30 * time.Millisecond)
	}
	stopWorker()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	readCourse, err := s.Read(ctx, p, "course")
	if err != nil || readCourse.Name != u2.Name {
		t.Fatalf("projected course did not reflect coalesced edit: %+v %v", readCourse, err)
	}

	// 3. HTTP GET & POST /api/v1/catalog/admin/schools/{school_id}/directory-projection.
	currentPrincipal := p
	h := httpHandlerForTest(s, func() c.Principal { return currentPrincipal })
	// Unauthorized / forbidden checks.
	currentPrincipal = c.Principal{}
	if code, _ := serveJSONRequest(h, "GET", "/api/v1/catalog/admin/schools/school/directory-projection", "", ""); code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	currentPrincipal = c.Principal{Subject: "student"}
	if code, _ := serveJSONRequest(h, "GET", "/api/v1/catalog/admin/schools/school/directory-projection", "", ""); code != 403 {
		t.Fatalf("expected 403, got %d", code)
	}
	if code, _ := serveJSONRequest(h, "POST", "/api/v1/catalog/admin/schools/school/directory-projection", "k1", `{}`); code != 403 {
		t.Fatalf("expected 403 on POST, got %d", code)
	}

	currentPrincipal = p
	code, body := serveJSONRequest(h, "POST", "/api/v1/catalog/admin/schools/school/directory-projection", "policy-job-1", `{"policy":{"cutoff_year":2024,"cutoff_season":"spring"}}`)
	if code != 202 {
		t.Fatalf("expected 202, got %d %s", code, body)
	}
	// Idempotent replay returns 202 with the same run; different payload returns 409.
	replayCode, _ := serveJSONRequest(h, "POST", "/api/v1/catalog/admin/schools/school/directory-projection", "policy-job-1", `{"policy":{"cutoff_year":2024,"cutoff_season":"spring"}}`)
	if replayCode != 202 {
		t.Fatalf("expected 202 on replay, got %d", replayCode)
	}
	conflictCode, _ := serveJSONRequest(h, "POST", "/api/v1/catalog/admin/schools/school/directory-projection", "policy-job-1", `{"policy":{"cutoff_year":2023,"cutoff_season":"autumn"}}`)
	if conflictCode != 409 {
		t.Fatalf("expected 409 on mismatched payload, got %d", conflictCode)
	}
	competingCode, _ := serveJSONRequest(h, "POST", "/api/v1/catalog/admin/schools/school/directory-projection", "policy-job-2", `{}`)
	if competingCode != 409 {
		t.Fatalf("expected 409 while job active, got %d", competingCode)
	}

	finished, err := s.ExecuteDirectoryRebuild(ctx, p, "school")
	if err != nil || finished == nil || finished.Status != "succeeded" || finished.Version == nil || *finished.Version <= first.Version {
		t.Fatalf("execute queued rebuild failed: %+v %v", finished, err)
	}
	getCode, getBody := serveJSONRequest(h, "GET", "/api/v1/catalog/admin/schools/school/directory-projection", "", "")
	if getCode != 200 {
		t.Fatalf("expected 200 on status GET, got %d %s", getCode, getBody)
	}
	finalStatus, err := s.AdminDirectoryProjection(ctx, p, "school")
	if err != nil || finalStatus.Published == nil || finalStatus.Published.Policy.CutoffYear != 2024 || finalStatus.LatestRun == nil || finalStatus.LatestRun.Status != "succeeded" {
		t.Fatalf("unexpected final status: %+v %v", finalStatus, err)
	}
}

func TestExpiredProjectionWorkerCannotPublishOverReplacement(t *testing.T) {
	s, p, db, before := projectionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var now atomic.Int64
	now.Store(time.Now().Unix())
	s.Clock = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	run, err := s.QueueDirectoryRebuild(ctx, p, "school", "old-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	var pid int
	if err := writer.QueryRowContext(ctx, "SELECT pg_backend_pid() FROM pg_advisory_xact_lock(hashtextextended('catalog:projection-source:school',0))").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.ExecuteDirectoryRebuild(ctx, p, "school")
		done <- err
	}()
	for {
		var blocked bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	now.Store(run.DeadlineAt.Unix() + 1)
	replacement, err := s.QueueDirectoryRebuild(ctx, p, "school", "replacement", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("expired worker succeeded")
	}
	status, err := s.AdminDirectoryProjection(ctx, p, "school")
	if err != nil || status.Published == nil || status.Published.Version != before.Version || status.LatestRun == nil || status.LatestRun.ID != replacement.ID || status.LatestRun.Status != "queued" {
		t.Fatal("expired worker changed the published version or replacement", status, err)
	}
}

func TestProjectionAndTeacherMarkersIsolateDiscoveryAndActivityVisibility(t *testing.T) {
	s, p, admin, first := projectionFixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	now := time.Now().UTC().Add(time.Minute)
	s.Clock = func() time.Time { return now }

	// Drain initial outbox so we observe only post-projection events.
	if _, err := admin.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}

	// Record views on visible course and official-only teacher so activity_buckets.visible = true.
	reader := c.Principal{Subject: "issuer:student"}
	for _, id := range []string{"course", "official"} {
		if _, err := s.Read(ctx, reader, id); err != nil {
			t.Fatal(id, err)
		}
	}
	if _, err := admin.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}

	// Configure ExcludedCourseNames before running batch import so leak-excluded and pre-cutoff
	// courses are never part of any published projection.
	policy := c.DefaultDirectoryPolicy()
	policy.ExcludedCourseNames = []string{"Excluded Secret"}
	report, err := s.RebuildDirectory(ctx, p, "school", &policy)
	if err != nil || report.Version <= first.Version {
		t.Fatal(report, err)
	}
	if _, err := admin.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}

	// A-01(1,2): An interrupted RunDirectoryImport page (including pre-cutoff / excluded courses)
	// must NOT emit premature catalog.discovery.v1 events to outbox.
	interrupted := errors.New("interrupted")
	partial := c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: now, Objects: []c.Object{
		{ID: "leak-pending", Kind: "course", SchoolID: "school", Name: "Pending Leak", Aliases: []string{}},
		{ID: "leak-excluded", Kind: "course", SchoolID: "school", Name: "Excluded Secret", Aliases: []string{}},
		{ID: "leak-excluded-off", Kind: "term_course", SchoolID: "school", Name: "Excluded Off", CourseID: "leak-excluded", TermID: "z-boundary", Aliases: []string{}},
		{ID: "leak-precutoff", Kind: "course", SchoolID: "school", Name: "Precutoff Course", Aliases: []string{}},
		{ID: "leak-precutoff-off", Kind: "term_course", SchoolID: "school", Name: "Precutoff Off", CourseID: "leak-precutoff", TermID: "a-old", Aliases: []string{}},
	}}
	if _, err := s.RunDirectoryImport(ctx, p, "school", "batch-leak", func(call context.Context) error {
		if _, err := s.Import(call, p, "page-1", partial); err != nil {
			return err
		}
		return interrupted
	}); !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	pending, err := repo.PendingOutbox(ctx, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range pending {
		if ev.Type == "catalog.discovery.v1" {
			t.Fatalf("interrupted directory import leaked discovery event: %+v", ev)
		}
	}

	// Complete the import batch and verify excluded/pre-cutoff courses never emit Visible=true,
	// while newly visible course emits only projection-revision discovery event.
	if _, err := s.RunDirectoryImport(ctx, p, "school", "batch-leak", func(call context.Context) error {
		if _, err := s.Import(call, p, "page-1", partial); err != nil {
			return err
		}
		_, err := s.Import(call, p, "page-2", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: now, Objects: []c.Object{
			{ID: "leak-pending-off", Kind: "term_course", SchoolID: "school", Name: "Pending Off", CourseID: "leak-pending", TermID: "z-boundary", Aliases: []string{}},
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var excludedVisible int
	if err := admin.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.discovery.v1' AND data->>'subject' IN ('leak-excluded','leak-precutoff') AND (data->'data'->>'visible')::boolean`).Scan(&excludedVisible); err != nil || excludedVisible != 0 {
		t.Fatalf("excluded/pre-cutoff course leaked Visible=true to outbox: count=%d err=%v", excludedVisible, err)
	}

	// A-01(4): Correct on a projected school must NOT emit dual small-integer + projection-version revisions.
	if _, err := admin.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	cur, err := s.AdminRead(ctx, p, "course")
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "Current Renamed"
	if _, err := s.Correct(ctx, p, "course", "rename-proj", c.Correction{Revision: cur.Revision, Name: &newTitle}); err != nil {
		t.Fatal(err)
	}
	pending, err = repo.PendingOutbox(ctx, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	courseDiscoveryEvents := 0
	for _, ev := range pending {
		if ev.Type == "catalog.discovery.v1" && ev.Subject == "course" {
			courseDiscoveryEvents++
			if ev.Data.Revision < first.Version {
				t.Fatalf("source small-integer revision leaked in projection mode: %+v", ev)
			}
		}
	}
	if courseDiscoveryEvents != 1 {
		t.Fatalf("expected exactly 1 projection discovery event for course, got %d", courseDiscoveryEvents)
	}

	// A-10: Hiding course via ExcludedCourseNames and revoking official teacher marker
	// must flip activity_buckets.visible to false and emit Visible=false activity events.
	if _, err := admin.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.TeacherCandidates(ctx, p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{
		SchoolID: "school", SourceID: "official", Revision: 2, ObservedAt: now,
		CandidateFingerprint: c.TeacherCandidateFingerprint(candidates), Matches: nil,
	}); err != nil {
		t.Fatal(err)
	}
	policy.ExcludedCourseNames = []string{"Excluded Secret", "Current Renamed"}
	if _, err := s.RebuildDirectory(ctx, p, "school", &policy); err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"course", "course"}, {"teacher", "official"}} {
		actID := c.ActivityID(target[0], target[1], now)
		act, err := repo.ReadActivity(ctx, actID)
		if err != nil || act.Visible || act.Metrics["views"] != 1 {
			t.Fatalf("hidden projected target %v retained visible activity bucket: %+v %v", target, act, err)
		}
		if _, ok, err := s.CurrentActivity(ctx, reader, actID); err != nil || ok {
			t.Fatalf("hidden target exposed by CurrentActivity: %v %v", ok, err)
		}
	}

	// A-01(3) & A-10 (non-projection RequireTeacherDirectory mode):
	// Importing an unmarked teacher must not emit Visible=true, and revoking official mark updates discovery & activity.
	s2, p2, siteSnap := teacherDirectoryFixture(t)
	repo2 := s2.Repo.(*postgres.Repository)
	s2.Clock = func() time.Time { return now }
	if _, err := s2.RefreshTeachingDirectory(ctx, p2, "school"); err != nil {
		t.Fatal(err)
	}
	siteSnap.ObservedAt = now
	if _, err := s2.ImportTeacherSites(ctx, p2, siteSnap); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Read(ctx, reader, "teacher3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Import(ctx, p2, "unmarked-t5", c.Import{SchoolID: "school", SourceID: "approved-source", Revision: 2, FetchedAt: now, Objects: []c.Object{
		{ID: "teacher5", Kind: "teacher", SchoolID: "school", Name: "Unmarked Five", DepartmentID: "dept", Aliases: []string{}},
	}}); err != nil {
		t.Fatal(err)
	}
	var unmarkedVisible int
	if err := repo2.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.discovery.v1' AND data->>'subject'='teacher5' AND (data->'data'->>'visible')::boolean`).Scan(&unmarkedVisible); err != nil || unmarkedVisible != 0 {
		t.Fatalf("unmarked teacher5 emitted Visible=true to outbox: %d %v", unmarkedVisible, err)
	}
	snapPage, err := repo2.DiscoverySnapshot(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range snapPage.Items {
		if doc.Target.ID == "teacher5" && doc.Visible {
			t.Fatalf("unmarked teacher5 visible in DiscoverySnapshot: %+v", doc)
		}
	}
	cands2, err := s2.TeacherCandidates(ctx, p2, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ImportTeacherSites(ctx, p2, c.TeacherSiteSnapshot{
		SchoolID: "school", SourceID: "official", Revision: 2, ObservedAt: now,
		CandidateFingerprint: c.TeacherCandidateFingerprint(cands2), Matches: nil,
	}); err != nil {
		t.Fatal(err)
	}
	act3, err := repo2.ReadActivity(ctx, c.ActivityID("teacher", "teacher3", now))
	if err != nil || act3.Visible || act3.Metrics["views"] != 1 {
		t.Fatalf("revoked official teacher3 retained visible activity in non-projection mode: %+v %v", act3, err)
	}
	snapPage, err = repo2.DiscoverySnapshot(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range snapPage.Items {
		if doc.Target.ID == "teacher3" && doc.Visible {
			t.Fatalf("revoked official teacher3 remained visible in DiscoverySnapshot: %+v", doc)
		}
	}
}
