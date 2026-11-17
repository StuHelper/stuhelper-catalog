package catalog_test

import (
	"context"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func identityFixture(t *testing.T) (*c.Service, c.Principal, c.CourseIdentities) {
	t.Helper()
	s, p, in := fixture(t)
	for i := range in.Objects {
		switch in.Objects[i].ID {
		case "course":
			in.Objects[i].Code = "OLD001"
			in.Objects[i].Name = "原课程名"
			in.Objects[i].EnglishName = "Old English"
		case "dept":
			in.Objects[i].Aliases = []string{"学院曾用名"}
		}
	}
	credits, hours := 2.0, 32.0
	in.Objects = append(in.Objects, c.Object{ID: "new-course", Kind: "course", SchoolID: "school", Name: "新课程名", Code: "NEW001", EnglishName: "New English", Credits: &credits, Hours: &hours, DepartmentID: "dept", Aliases: []string{}})
	if _, err := s.Import(context.Background(), p, "seed-identities", in); err != nil {
		t.Fatal(err)
	}
	return s, p, c.CourseIdentities{SchoolID: "school", SourceID: in.SourceID, Revision: in.Revision, Mappings: []c.CourseIdentityMapping{{SourceID: "course", TargetID: "new-course"}}}
}

func TestCourseIdentityMergedCutoffSearchRelationsAndRecentVisits(t *testing.T) {
	s, p, input := identityFixture(t)
	ctx := context.Background()
	if err := s.SaveTermOrders(ctx, p, "school", []c.TermOrder{{ID: "term1", Year: 2023, Season: "autumn"}, {ID: "term2", Year: 2022, Season: "autumn"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRecentCourse(ctx, c.Principal{Subject: "issuer:student"}, "course"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RebuildDirectory(ctx, p, "school", nil); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	repo.RequireDirectoryProjection = true
	s.DirectoryMetrics = uiMetrics{}
	if _, err := s.Get(ctx, "new-course"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("new code should initially lack recent offering", err)
	}
	result, err := s.ApplyCourseIdentities(ctx, p, "identities", input)
	if err != nil || result.Mappings != 1 || result.Redirects != 1 {
		t.Fatal(result, err)
	}
	for _, id := range []string{"course", "new-course"} {
		resolved, err := s.ReadResolution(ctx, c.Principal{Subject: "issuer:student"}, id)
		if err != nil || len(resolved.IDs) != 2 || resolved.Object.Code != "NEW001" || resolved.Object.Name != "新课程名" || resolved.Object.EnglishName != "New English" || resolved.Object.Credits == nil || *resolved.Object.Credits != 2 || resolved.Object.Hours == nil || *resolved.Object.Hours != 32 || !slices.Equal(resolved.Object.FormerCodes, []string{"OLD001"}) || !slices.Equal(resolved.Object.FormerNames, []string{"原课程名"}) || len(resolved.Object.Aliases) != 0 {
			t.Fatal(resolved, err)
		}
		if id == "course" && resolved.Object.CanonicalID != "new-course" {
			t.Fatal("old URL unresolved")
		}
	}
	for _, query := range []string{"新课程名", "原课程名", "NEW001", "old001", "NEW ENGLISH"} {
		page, err := s.ReadDirectory(ctx, c.Principal{Subject: "issuer:student"}, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Query: query, Limit: 20}, Sort: "name"})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Object.ID != "new-course" {
			t.Fatal(query, page, err)
		}
		list, err := s.List(ctx, c.Filter{Kind: "course", SchoolID: "school", Query: query, Limit: 20})
		if err != nil || len(list.Items) != 1 || list.Items[0].ID != "new-course" {
			t.Fatal(query, list, err)
		}
	}
	for _, id := range []string{"course", "new-course"} {
		enrollments, err := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", id, "term1", "", 20)
		if err != nil || len(enrollments) != 1 || enrollments[0].CourseID != "new-course" || len(enrollments[0].TeacherIDs) != 2 {
			t.Fatal(enrollments, err)
		}
	}
	if err := s.RecordRecentCourse(ctx, c.Principal{Subject: "issuer:student"}, "new-course"); err != nil {
		t.Fatal(err)
	}
	recent, err := s.MyRecentCourses(ctx, c.Principal{Subject: "issuer:student"})
	if err != nil || len(recent.Items) != 1 || recent.Items[0].Course.ID != "new-course" {
		t.Fatal(recent, err)
	}
	alias, err := s.AdminRead(ctx, p, "dept")
	if err != nil || !slices.Equal(alias.Aliases, []string{"学院曾用名"}) {
		t.Fatal(alias, err)
	}
	docs, err := s.DiscoveryCurrent(ctx, c.Principal{Subject: "issuer:student"}, []string{"course", "new-course"})
	if err != nil || len(docs) != 1 || docs[0].Code != "NEW001" || docs[0].EnglishName != "New English" || len(docs[0].FormerCodes) != 1 {
		t.Fatal(docs, err)
	}
}

func TestCourseIdentityConcurrentReplayAndInvalidMappingsAreAtomic(t *testing.T) {
	s, p, input := identityFixture(t)
	ctx := context.Background()
	if _, err := s.ApplyCourseIdentities(ctx, c.Principal{Subject: "unauthorized", CanImport: true, Schools: []string{"elsewhere"}}, "denied", input); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	bad := input
	bad.Mappings = append(slices.Clone(input.Mappings), c.CourseIdentityMapping{SourceID: "absent", TargetID: "other"})
	if _, err := s.ApplyCourseIdentities(ctx, p, "missing", bad); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	old, err := s.Get(ctx, "course")
	if err != nil || old.CanonicalID != "" {
		t.Fatal("partial mapping committed", old, err)
	}
	for _, pairs := range [][]c.CourseIdentityMapping{
		{{SourceID: "course", TargetID: "new-course"}, {SourceID: "new-course", TargetID: "another"}},
		{{SourceID: "course", TargetID: "new-course"}, {SourceID: "another", TargetID: "new-course"}},
		{{SourceID: "course", TargetID: "course"}},
	} {
		bad := input
		bad.Mappings = pairs
		if _, err := s.ApplyCourseIdentities(ctx, p, "invalid", bad); !errors.Is(err, c.ErrInput) {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ApplyCourseIdentities(cancelled, p, "cancelled", input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan c.CourseIdentitiesResult, 6)
	failures := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			out, err := s.ApplyCourseIdentities(ctx, p, "concurrent", input)
			results <- out
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first c.CourseIdentitiesResult
	for result := range results {
		if first.ID == "" {
			first = result
		}
		if result != first {
			t.Fatal("replay differs", result, first)
		}
	}
	s.Clock = func() time.Time { return time.Now().Add(72 * time.Hour) }
	again, err := s.ApplyCourseIdentities(ctx, p, "concurrent", input)
	if err != nil || again != first {
		t.Fatal("durable replay failed", again, err)
	}
	bad = input
	bad.Revision++
	if _, err := s.ApplyCourseIdentities(ctx, p, "concurrent", bad); !errors.Is(err, c.ErrConflict) {
		t.Fatal("same key different content accepted", err)
	}
	old, err = s.Get(ctx, "course")
	if err != nil || old.CanonicalID != "new-course" || old.Revision != 2 {
		t.Fatal(old, err)
	}
	if _, err := s.Import(ctx, p, "third", c.Import{SchoolID: input.SchoolID, SourceID: input.SourceID, Revision: input.Revision, FetchedAt: time.Now(), Objects: []c.Object{{ID: "third-course", Kind: "course", SchoolID: input.SchoolID, Name: "Third", Code: "THIRD"}}}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []c.CourseIdentityMapping{{SourceID: "third-course", TargetID: "new-course"}, {SourceID: "new-course", TargetID: "third-course"}} {
		bad := input
		bad.Mappings = []c.CourseIdentityMapping{pair}
		if _, err := s.ApplyCourseIdentities(ctx, p, "cross-request", bad); !errors.Is(err, c.ErrConflict) {
			t.Fatal("conflict with an earlier mapping accepted", pair, err)
		}
	}
}

func TestCourseNamesMigrateWithoutLosingDepartmentAliasesOrNewWrites(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	for i := range in.Objects {
		if in.Objects[i].Kind == "course" {
			in.Objects[i].Aliases = []string{"Legacy English"}
		}
		if in.Objects[i].Kind == "department" {
			in.Objects[i].Aliases = []string{"Former Department"}
		}
	}
	if _, err := s.Import(ctx, p, "legacy", in); err != nil {
		t.Fatal(err)
	}
	in.Revision = 2
	in.SeparateCourseNames = true
	for i := range in.Objects {
		if in.Objects[i].Kind == "course" {
			in.Objects[i].Aliases = []string{}
			in.Objects[i].EnglishName = "Current English"
			in.Objects[i].Name = "Updated course"
		}
	}
	if _, err := s.Import(ctx, p, "separate", in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "course")
	if err != nil || got.EnglishName != "Current English" || len(got.Aliases) != 0 || !reflect.DeepEqual(got.FormerNames, []string{"course"}) {
		t.Fatal(got, err)
	}
	// A post-cutover enrollment remains in the same database through repeated
	// additive migrations and source replay. No snapshot is restored.
	in.Objects = nil
	in.Enrollments[0].ID = "new-enrollment"
	in.Enrollments[0].Subject = "issuer:new-student"
	if _, err := s.Import(ctx, p, "new-write", in); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	var count int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM catalog.enrollments`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	department, err := s.Get(ctx, "dept")
	if err != nil || !slices.Equal(department.Aliases, []string{"Former Department"}) {
		t.Fatal(department, err)
	}
}
