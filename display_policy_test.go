package catalog_test

import (
	"context"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"slices"
	"testing"
	"time"
)

func TestDirectoryDisplayRulesProjectsCountsAndEnrollmentOwnership(t *testing.T) {
	s, p, db, _ := projectionFixture(t)
	ctx := context.Background()
	objects := []c.Object{
		{ID: "sports", Kind: "category", Name: "体育类"}, {ID: "sports-other", Kind: "category", Name: "体育课"},
		{ID: "foreign", Kind: "category", Name: "外语类"}, {ID: "foreign-other", Kind: "category", Name: "外语类课"},
		{ID: "liberal", Kind: "category", Name: "博雅类"}, {ID: "elective", Kind: "category", Name: "公共选修课程"}, {ID: "office-cat", Kind: "category", Name: "Office Hours"},
		{ID: "sports-teacher-a", Kind: "teacher", Name: "Synthetic sports A"}, {ID: "sports-teacher-b", Kind: "teacher", Name: "Synthetic sports B"},
	}
	add := func(id, name, category string) {
		objects = append(objects, c.Object{ID: id, Kind: "course", Name: name}, c.Object{ID: id + "-offering", Kind: "term_course", Name: name, CourseID: id, TermID: "z-boundary"}, c.Object{ID: id + "-class", Kind: "class", Name: "Synthetic class", TermCourseID: id + "-offering", CategoryID: category})
	}
	add("office", " OFFICE HOURS ", "")
	add("office-category", "Synthetic consultation", "office-cat")
	add("liberal-course", "Synthetic liberal", "liberal")
	add("elective-course", "Synthetic elective", "elective")
	add("language-a", "Synthetic language A", "foreign")
	add("language-b", "Synthetic language B", "foreign-other")
	// A historical category must not hide a course whose current class is allowed.
	objects = append(objects, c.Object{ID: "language-old-offering", Kind: "term_course", Name: "Old", CourseID: "language-a", TermID: "a-old"}, c.Object{ID: "language-old-class", Kind: "class", Name: "Old", TermCourseID: "language-old-offering", CategoryID: "elective"})
	add("semester1", "体育（1）", "sports")
	add("semester2", "体育（2）", "sports-other")
	for i := range objects {
		switch objects[i].ID {
		case "semester1-class":
			objects[i].SportsProjectCode = "tt1"
		case "semester2-class":
			objects[i].SportsProjectCode = "tt2"
		}
	}
	objects = append(objects,
		c.Object{ID: "swim-class", Kind: "class", Name: "Synthetic swim class", TermCourseID: "semester1-offering", CategoryID: "sports", SportsProjectCode: "swim"},
		c.Object{ID: "unknown-class", Kind: "class", Name: "乒乓球场地名称不能作为项目", TermCourseID: "semester1-offering", CategoryID: "sports", SportsProjectCode: "unknown"},
		c.Object{ID: "missing-class", Kind: "class", Name: "Synthetic missing class", TermCourseID: "semester2-offering", CategoryID: "sports"},
		c.Object{ID: "no-teacher-class", Kind: "class", Name: "Synthetic unassigned", TermCourseID: "semester1-offering", CategoryID: "sports", SportsProjectCode: "swim"},
		c.Object{ID: "sport-a", Kind: "assignment", Name: "Teaching", ClassID: "semester1-class", TeacherID: "sports-teacher-a"},
		c.Object{ID: "sport-b", Kind: "assignment", Name: "Teaching", ClassID: "semester2-class", TeacherID: "sports-teacher-b"},
		c.Object{ID: "sport-swim", Kind: "assignment", Name: "Teaching", ClassID: "swim-class", TeacherID: "sports-teacher-b"},
		c.Object{ID: "sport-unknown", Kind: "assignment", Name: "Teaching", ClassID: "unknown-class", TeacherID: "sports-teacher-a"},
	)
	objects = append(objects, c.Object{ID: "sports-old-offering", Kind: "term_course", Name: "Old sports", CourseID: "semester1", TermID: "a-old"},
		c.Object{ID: "sports-old-class", Kind: "class", Name: "Old class", TermCourseID: "sports-old-offering", CategoryID: "sports-other", SportsProjectCode: "tt1"},
		c.Object{ID: "sports-old-assignment", Kind: "assignment", Name: "Old teaching", ClassID: "sports-old-class", TeacherID: "legacy"})
	for i := range objects {
		objects[i].SchoolID = "school"
		objects[i].Aliases = []string{}
	}
	enrollments := []c.Enrollment{{ID: "sports-enrollment", Subject: "issuer:student", SchoolID: "school", CourseID: "semester1", TermID: "z-boundary", ClassID: "semester1-class", TeacherIDs: []string{"sports-teacher-a"}, Active: true}}
	if _, err := s.Import(c.DeferDirectoryRebuild(ctx), p, "display-seed", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: objects, Enrollments: enrollments}); err != nil {
		t.Fatal(err)
	}
	policy := c.DefaultDirectoryPolicy()
	policy.ExcludedCourseNames = []string{"office hours"}
	policy.ExcludedCategoryNames = []string{"office hours", "博雅类", "公共选修课程"}
	policy.CategoryAliases = []c.CategoryAlias{{From: "体育课", To: "体育类"}, {From: "外语类课", To: "外语类"}}
	policy.Sports = &c.SportsDirectoryPolicy{CategoryName: "体育类", CourseNames: []string{"体育（1）", "体育（2）"}, Projects: []c.SportsProject{{Key: "table-tennis", Name: "乒乓球", SourceCodes: []string{"tt1", "tt2"}}, {Key: "swimming", Name: "游泳", SourceCodes: []string{"swim"}}}}
	report, err := s.RebuildDirectory(ctx, p, "school", &policy)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExcludedCourses != 4 || report.SportsSourceCourses != 2 || report.SportsProjects != 2 || report.SportsClasses != 6 || report.UnresolvedSportsClasses != 3 || report.SportsUnresolvedReasons["missing_project_code"] != 1 || report.SportsUnresolvedReasons["unknown_project_code"] != 1 || report.SportsUnresolvedReasons["missing_teacher"] != 1 {
		t.Fatalf("report: %+v", report)
	}
	tt, swim := c.SportsProjectID("school", "table-tennis"), c.SportsProjectID("school", "swimming")
	for _, id := range []string{"office", "office-category", "liberal-course", "elective-course", "semester1", "semester2", "unknown-class", "missing-class", "no-teacher-class", "semester1-offering"} {
		if _, err := s.Read(ctx, p, id); !errors.Is(err, c.ErrNotFound) {
			t.Fatalf("excluded detail %s: %v", id, err)
		}
	}
	summary, err := s.ReadSummary(ctx, p, tt)
	if err != nil || summary.TeacherCount != 2 || summary.Object.CourseType != "sports_project" || summary.Object.LatestTermID != "z-boundary" {
		t.Fatal(summary, err)
	}
	if _, err := s.Read(ctx, p, c.SportsOfferingID(tt, "z-boundary")); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("sports offering exposed by public read", err)
	}
	internalSummary, err := s.Summary(ctx, tt)
	if err != nil || internalSummary.OfferingCount != 1 {
		t.Fatal("trusted sports offering count", internalSummary, err)
	}
	offering, err := s.Get(ctx, c.SportsOfferingID(tt, "z-boundary"))
	if err != nil || offering.CourseID != tt || offering.TermID != "z-boundary" {
		t.Fatal(offering, err)
	}
	teachers, err := s.ReadList(ctx, p, c.Filter{Kind: "teacher", CourseID: tt, Limit: 100})
	if err != nil || len(teachers.Items) != 2 {
		t.Fatal(teachers, err)
	}
	for _, sort := range []string{"name", "hot", "recommend", "reviews"} {
		page, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 100}, Sort: sort})
		if err != nil || page.Total != 5 {
			t.Fatal(sort, page, err)
		}
	}
	options, err := s.ReadFilterOptions(ctx, p, "course", "school")
	if err != nil || options.Total != 5 {
		t.Fatal(options, err)
	}
	for _, option := range options.Categories {
		if slices.Contains([]string{"体育课", "外语类课", "博雅类", "公共选修课程", "Office Hours"}, option.Name) {
			t.Fatal("unmerged option", option)
		}
		page, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: "course", CategoryID: option.ID, Limit: 100}, Sort: "name"})
		if err != nil || option.Count != page.Total {
			t.Fatal("facet mismatch", option, page, err)
		}
		if (option.Name == "体育类" || option.Name == "外语类") && option.Count != 2 {
			t.Fatal(option)
		}
	}
	oldCategory, err := s.ReadList(ctx, p, c.Filter{Kind: "course", CategoryID: "sports-other", Limit: 100})
	if err != nil || len(oldCategory.Items) != 2 {
		t.Fatal("category alias", oldCategory, err)
	}
	student := c.Principal{Subject: "issuer:student"}
	evidence, err := s.ReviewEnrollments(ctx, student, tt, "z-boundary", "", 100)
	if err != nil || len(evidence) != 1 || evidence[0].CourseID != tt || !slices.Equal(evidence[0].TeacherIDs, []string{"sports-teacher-a"}) {
		t.Fatal(evidence, err)
	}
	for _, target := range []string{swim} {
		ev, err := s.ReviewEnrollments(ctx, student, target, "z-boundary", "", 100)
		if err != nil || len(ev) != 0 {
			t.Fatal("another project evidence", ev, err)
		}
	}
	if ev, err := s.ReviewEnrollments(ctx, c.Principal{Subject: "issuer:other"}, tt, "z-boundary", "", 100); err != nil || len(ev) != 0 {
		t.Fatal("another owner", ev, err)
	}
	var sourceCourse string
	if err := db.QueryRow(`SELECT course_id FROM catalog.enrollments WHERE id='sports-enrollment'`).Scan(&sourceCourse); err != nil || sourceCourse != "semester1" {
		t.Fatal("source enrollment changed", sourceCourse, err)
	}
	if ev, err := s.ReviewEnrollments(ctx, student, "semester1", "z-boundary", "", 100); !errors.Is(err, c.ErrNotFound) || len(ev) != 0 {
		t.Fatal("old target allowed", ev, err)
	}
	refs, err := s.MyReferences(ctx, student, []string{tt})
	if err != nil || len(refs.Items) != 1 || refs.Items[0].Name != "乒乓球" {
		t.Fatal(refs, err)
	}
	if yes, err := s.HasTaught(ctx, []string{"sports-teacher-a"}, tt); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if yes, err := s.HasTaught(ctx, []string{"legacy"}, tt); err != nil || !yes {
		t.Fatal("historical project teaching lost", yes, err)
	}
	if yes, err := s.HasTaught(ctx, []string{"sports-teacher-a"}, swim); err != nil || yes {
		t.Fatal(yes, err)
	}
	again, err := s.RebuildDirectory(ctx, p, "school", nil)
	if err != nil || again.Courses != report.Courses || again.UnresolvedSportsClasses != 3 || !again.Policy.Equal(policy) {
		t.Fatal(again, err)
	}
	s.EnrollmentSource = sourceStub{snapshot: c.EnrollmentSnapshot{SchoolID: "school", SourceID: "source", Version: "sports-current", Revision: 3, FetchedAt: time.Now(), Enrollments: enrollments}}
	s.OwnSummaryAccess = ownSummaryPermission{}
	candidates, err := s.MyReviewCandidates(ctx, student)
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].CourseID != tt {
		t.Fatal("pending project candidate", candidates, err)
	}
	s.OwnSummaryAccess = ownSummaryPermission{err: c.ErrForbidden}
	if _, err = s.MyReviewCandidates(ctx, student); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("revoked summary access", err)
	}
	// No source rows are rewritten or duplicated by rebuilds.
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM catalog.enrollments WHERE id='sports-enrollment' AND course_id='semester1'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestDirectoryPolicyRejectsAmbiguousMappingsAndReadOnlyObjectFields(t *testing.T) {
	p := c.DefaultDirectoryPolicy()
	p.Sports = &c.SportsDirectoryPolicy{CategoryName: "Sports", Projects: []c.SportsProject{{Key: "a", Name: "A", SourceCodes: []string{"one"}}, {Key: "b", Name: "B", SourceCodes: []string{"one"}}}}
	if p.Valid() {
		t.Fatal("ambiguous source code accepted")
	}
	p = c.DefaultDirectoryPolicy()
	p.CategoryAliases = []c.CategoryAlias{{From: "A", To: "B"}, {From: "B", To: "C"}}
	if p.Valid() {
		t.Fatal("alias chain accepted")
	}
	p = c.DefaultDirectoryPolicy()
	p.ExcludedCourseNames = []string{"Office Hours", "office hours"}
	if p.Valid() {
		t.Fatal("duplicate normalized names accepted")
	}
	s, admin, in := fixture(t)
	in.Objects[0].CourseType = "sports_project"
	if _, err := s.Import(context.Background(), admin, "forged-project", in); !errors.Is(err, c.ErrInput) {
		t.Fatal(err)
	}
}

func TestDisplayProjectionMigrationPreservesPublishedDirectoryAndRuntimeBoundary(t *testing.T) {
	s, p, admin, before := projectionFixture(t)
	ctx := context.Background()
	// Model an existing installation before C2, while retaining its complete generation.
	if _, err := admin.Exec(`DROP TABLE catalog.projection_sports_classes,catalog.projection_sports_sources`); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	if err := postgres.Migrate(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, admin); err != nil {
		t.Fatal("migration replay", err)
	}
	after, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil || after.Version != before.Version || after.Courses != before.Courses {
		t.Fatal("published directory changed during migration", after, err)
	}
	if err := repo.CheckRuntime(ctx); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("new table grants were not required", err)
	}
	var role string
	if err := repo.DB.QueryRow(`SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON catalog.projection_sports_sources,catalog.projection_sports_classes TO ` + role); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB.Exec(`CREATE TABLE catalog.forbidden_c2_migration(id text)`); err == nil {
		t.Fatal("runtime obtained migration rights")
	}
}
