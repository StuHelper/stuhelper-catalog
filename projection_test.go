package catalog_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func projectionFixture(t *testing.T) (*c.Service, c.Principal, *sql.DB, c.ProjectionReport) {
	t.Helper()
	repo, admin := database(t)
	s := &c.Service{Repo: repo, ReadAccess: &readAccess{}, DirectoryMetrics: uiMetrics{}}
	p := c.Principal{Subject: "issuer:student", Schools: []string{"school"}, CanImport: true, CanMaintain: true}
	objects := []c.Object{{ID: "school", Kind: "school", Name: "Synthetic school"}, {ID: "dept", Kind: "department", Name: "Department"}, {ID: "unused", Kind: "category", Name: "Empty"},
		{ID: "category", Kind: "category", Name: "Visible category"}, {ID: "nature", Kind: "nature", Name: "Nature"},
		{ID: "course", Kind: "course", Name: "Current", DepartmentID: "dept"}, {ID: "old", Kind: "course", Name: "Old", DepartmentID: "dept", CategoryID: "unused"},
		{ID: "invalid", Kind: "course", Name: "Invalid", DepartmentID: "dept"},
		{ID: "teacher", Kind: "teacher", Name: "Teacher", DepartmentID: "dept"}, {ID: "legacy", Kind: "teacher", Name: "Legacy", DepartmentID: "dept"},
		{ID: "official", Kind: "teacher", Name: "Official", DepartmentID: "dept"},
		{ID: "a-old", Kind: "term", Name: "Unreliable display 2099 autumn"}, {ID: "z-boundary", Kind: "term", Name: "Cutoff"},
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
	terms := []c.TermOrder{{ID: "a-old", Year: 2023, Season: "summer"}, {ID: "z-boundary", Year: 2023, Season: "autumn"}, {ID: "z-spring", Year: 2024, Season: "spring"},
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

func TestDirectoryProjectionCutoffOrderingCountsAndArchivedDetails(t *testing.T) {
	s, p, _, report := projectionFixture(t)
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
		object, err := s.Read(ctx, p, id)
		if err != nil || !object.ReadOnly {
			t.Fatal("archive link", object, err)
		}
		page, err := s.ReadList(ctx, p, c.Filter{Kind: "course", Query: object.Name, Limit: 100})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("archive in search", page, err)
		}
	}
	summary, err := s.ReadSummary(ctx, p, "course")
	if err != nil || summary.OfferingCount != 4 || summary.TeacherCount != 1 || summary.CourseCount != 1 {
		t.Fatal("summary", summary, err)
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
	if yes, err := s.HasTaught(ctx, []string{"legacy"}, "old"); err != nil || !yes {
		t.Fatal("historic teacher disclosure evidence lost", yes, err)
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
	if err != nil || object.ReadOnly {
		t.Fatal(object, err)
	}
}
