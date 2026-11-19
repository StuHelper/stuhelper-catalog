package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
)

func TestDirectorySummaryLookupAndOfferingContext(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects, c.Object{ID: "meeting", Kind: "schedule", SchoolID: "school", Name: "Meeting", ClassID: "class1", TimeText: "Monday", Location: "Room A", Aliases: []string{}})
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(ctx, "course")
	if err != nil || summary.Object.Credits != nil || summary.School.ID != "school" || summary.Department == nil || summary.Department.ID != "dept" || summary.OfferingCount != 2 || summary.TeacherCount != 2 || len(summary.Categories) != 1 || summary.Categories[0].ID != "seminar" || len(summary.Natures) != 1 {
		t.Fatalf("summary: %+v %v", summary, err)
	}
	teacher, err := s.Summary(ctx, "teacher")
	if err != nil || teacher.CourseCount != 1 || teacher.OfferingCount != 1 || teacher.Department != nil {
		t.Fatalf("teacher summary: %+v %v", teacher, err)
	}
	for _, f := range []c.Filter{
		{Kind: "term_course", TeacherID: "teacher", Limit: 20},
		{Kind: "schedule", CourseID: "course", TermID: "term1", Limit: 20},
		{Kind: "schedule", TermCourseID: "offering1", ClassID: "class1", Limit: 20},
		{Kind: "assignment", CourseID: "course", TermID: "term1", TeacherID: "teacher", Limit: 20},
	} {
		page, err := s.List(ctx, f)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("context %+v: %+v %v", f, page, err)
		}
	}
	teachers, err := s.List(ctx, c.Filter{Kind: "teacher", ClassID: "class1", Limit: 20})
	if err != nil || len(teachers.Items) != 2 {
		t.Fatalf("co-teaching: %+v %v", teachers, err)
	}
	wrongTerm, err := s.List(ctx, c.Filter{Kind: "schedule", CourseID: "course", TermID: "term2", Limit: 20})
	if err != nil || len(wrongTerm.Items) != 0 {
		t.Fatal("another term leaked", wrongTerm, err)
	}
	batch, err := s.Lookup(ctx, []string{"teacher", "course", "teacher", "missing"})
	if err != nil || len(batch.Items) != 2 || len(batch.MissingIDs) != 1 || batch.MissingIDs[0] != "missing" {
		t.Fatal(batch, err)
	}
	if _, err = s.Lookup(ctx, make([]string, 101)); !errors.Is(err, c.ErrInput) {
		t.Fatal("unbounded lookup accepted")
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return c.Principal{Subject: "issuer:student"}, nil }}.Routes()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/catalog/objects/lookup?ids=course,missing", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var payload map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"issuer:student", "source_id", "fetched_at", "enrollments", "manual"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("private directory field leaked", private)
		}
	}
}

func TestSummaryAndEnrollmentKeepMergedRelations(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects,
		c.Object{ID: "copy", Kind: "course", SchoolID: "school", Name: "Copy", Aliases: []string{}},
		c.Object{ID: "copy-offering", Kind: "term_course", SchoolID: "school", Name: "Copy offering", CourseID: "copy", TermID: "term1", Aliases: []string{}},
		c.Object{ID: "copy-class", Kind: "class", SchoolID: "school", Name: "Copy class", TermCourseID: "copy-offering", Aliases: []string{}},
		c.Object{ID: "copy-assignment", Kind: "assignment", SchoolID: "school", Name: "Copy assignment", ClassID: "copy-class", TeacherID: "teacher2", Aliases: []string{}},
	)
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"course", "copy"}, {"teacher", "teacher2"}} {
		request := c.MergeInput{TargetID: pair[1], SourceRevision: 1, TargetRevision: 1, Reason: "Confirmed duplicate"}
		preview, err := s.PreviewMerge(ctx, p, pair[0], request)
		if err != nil {
			t.Fatal(err)
		}
		request.PreviewToken = preview.PreviewToken
		if _, err := s.Merge(ctx, p, pair[0], "merge-"+pair[0], request); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"course", "copy", "teacher", "teacher2"} {
		summary, err := s.Summary(ctx, id)
		wantOfferings := 2
		if strings.HasPrefix(id, "teacher") {
			wantOfferings = 1
		}
		if err != nil || summary.CourseCount != 1 || summary.TeacherCount != 1 || summary.OfferingCount != wantOfferings {
			t.Fatalf("merged summary %s: %+v %v", id, summary, err)
		}
		if wantOfferings == 2 && (len(summary.Categories) != 1 || summary.Categories[0].ID != "seminar" || len(summary.Natures) != 1) {
			t.Fatalf("merged classifications lost: %+v", summary)
		}
	}
	for _, filter := range []c.Filter{
		{Kind: "term_course", CourseID: "course", TeacherID: "teacher", Limit: 20},
		{Kind: "teacher", CourseID: "copy", TermCourseID: "offering1", Limit: 20},
		{Kind: "teacher", ClassID: "class1", Limit: 20},
		{Kind: "course", TeacherID: "teacher", Limit: 20},
	} {
		page, err := s.List(ctx, filter)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("merged relations %+v: %+v %v", filter, page, err)
		}
	}
	// Both historical and canonical teacher IDs must still resolve to the
	// student's actual class assignment, including collapsed duplicate edges.
	for i, teacher := range []string{"teacher", "teacher2"} {
		next := c.Import{SchoolID: in.SchoolID, SourceID: in.SourceID, Revision: int64(i + 2), FetchedAt: in.FetchedAt,
			Enrollments: []c.Enrollment{{ID: "enrollment", Subject: "issuer:student", SchoolID: "school", CourseID: "course", TermID: "term1", ClassID: "class1", TeacherIDs: []string{teacher}, Active: true}}}
		if _, err := s.Import(ctx, p, "refresh-"+teacher, next); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSummaryRelatedCourseAndTeacherPreview(t *testing.T) {
	repo, _ := database(t)
	repo.RequireTeacherDirectory = true
	s := &c.Service{Repo: repo, ReadAccess: &readAccess{}, DirectoryMetrics: uiMetrics{}}
	p := c.Principal{Subject: "issuer:student", Schools: []string{"school"}, CanImport: true, CanMaintain: true}
	ctx := context.Background()

	objects := []c.Object{
		{ID: "school", Kind: "school", SchoolID: "school", Name: "School", Aliases: []string{}},
		{ID: "dept", Kind: "department", SchoolID: "school", Name: "Department", Aliases: []string{}},
		{ID: "term", Kind: "term", SchoolID: "school", Name: "2024 Autumn", Aliases: []string{}},
		{ID: "course-a", Kind: "course", SchoolID: "school", Name: "Algorithms", DepartmentID: "dept", Aliases: []string{}},
		{ID: "course-b", Kind: "course", SchoolID: "school", Name: "Systems", DepartmentID: "dept", Aliases: []string{}},
		{ID: "offering-a", Kind: "term_course", SchoolID: "school", Name: "Algorithms Offering", CourseID: "course-a", TermID: "term", Aliases: []string{}},
		{ID: "offering-b", Kind: "term_course", SchoolID: "school", Name: "Systems Offering", CourseID: "course-b", TermID: "term", Aliases: []string{}},
		{ID: "class-a", Kind: "class", SchoolID: "school", Name: "Class A", TermCourseID: "offering-a", Aliases: []string{}},
		{ID: "class-b", Kind: "class", SchoolID: "school", Name: "Class B", TermCourseID: "offering-b", Aliases: []string{}},
		{ID: "teacher-official", Kind: "teacher", SchoolID: "school", Name: "Official Only", DepartmentID: "dept", Aliases: []string{}},
		{ID: "teacher-hidden", Kind: "teacher", SchoolID: "school", Name: "Hidden Unmarked", DepartmentID: "dept", Aliases: []string{}},
	}
	for i := 1; i <= 25; i++ {
		tid := fmt.Sprintf("teacher-%02d", i)
		objects = append(objects,
			c.Object{ID: tid, Kind: "teacher", SchoolID: "school", Name: fmt.Sprintf("Teacher %02d", i), DepartmentID: "dept", Aliases: []string{}},
			c.Object{ID: fmt.Sprintf("assign-a-%02d", i), Kind: "assignment", SchoolID: "school", Name: "Assign", ClassID: "class-a", TeacherID: tid, Aliases: []string{}},
		)
	}
	objects = append(objects, c.Object{ID: "assign-b-01", Kind: "assignment", SchoolID: "school", Name: "Assign B", ClassID: "class-b", TeacherID: "teacher-01", Aliases: []string{}})

	if _, err := s.Import(ctx, p, "seed", c.Import{SchoolID: "school", SourceID: "source", Revision: 1, FetchedAt: time.Now(), Objects: objects}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTermOrders(ctx, p, "school", []c.TermOrder{{ID: "term", Year: 2024, Season: "autumn"}}); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.TeacherCandidates(ctx, p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportTeacherSites(ctx, p, c.TeacherSiteSnapshot{
		CandidateFingerprint: c.TeacherCandidateFingerprint(candidates),
		SchoolID:             "school", SourceID: "sites", Revision: 1, ObservedAt: time.Now().UTC(),
		Matches: []c.TeacherSiteMatch{{TeacherID: "teacher-official", Name: "Official Only", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/official/zh_CN/index.htm"}},
	}); err != nil {
		t.Fatal(err)
	}

	checkMode := func(mode string) {
		t.Helper()
		courseSum, err := s.ReadSummary(ctx, p, "course-a")
		if err != nil {
			t.Fatalf("%s course ReadSummary: %v", mode, err)
		}
		if courseSum.TeacherCount != 25 || len(courseSum.Teachers) != 20 || len(courseSum.Courses) != 0 {
			t.Fatalf("%s course preview mismatch: count=%d teachers=%d courses=%d", mode, courseSum.TeacherCount, len(courseSum.Teachers), len(courseSum.Courses))
		}
		if courseSum.Teachers[0].ID != "teacher-01" || courseSum.Teachers[0].Kind != "teacher" || courseSum.Teachers[0].Name != "Teacher 01" || courseSum.Teachers[19].ID != "teacher-20" {
			t.Fatalf("%s course teachers order/fields: first=%+v last=%+v", mode, courseSum.Teachers[0], courseSum.Teachers[19])
		}

		teacherSum, err := s.ReadSummary(ctx, p, "teacher-01")
		if err != nil {
			t.Fatalf("%s teacher ReadSummary: %v", mode, err)
		}
		if teacherSum.CourseCount != 2 || len(teacherSum.Courses) != 2 || len(teacherSum.Teachers) != 0 {
			t.Fatalf("%s teacher preview mismatch: count=%d courses=%+v teachers=%+v", mode, teacherSum.CourseCount, teacherSum.Courses, teacherSum.Teachers)
		}
		if teacherSum.Courses[0].ID != "course-a" || teacherSum.Courses[0].Name != "Algorithms" || teacherSum.Courses[1].ID != "course-b" || teacherSum.Courses[1].Name != "Systems" {
			t.Fatalf("%s teacher courses order/fields: %+v", mode, teacherSum.Courses)
		}

		officialSum, err := s.ReadSummary(ctx, p, "teacher-official")
		if err != nil {
			t.Fatalf("%s official teacher ReadSummary: %v", mode, err)
		}
		if officialSum.CourseCount != 0 || officialSum.Courses == nil || len(officialSum.Courses) != 0 || officialSum.Teachers == nil || len(officialSum.Teachers) != 0 {
			t.Fatalf("%s official-only teacher should have empty non-nil slices: %+v", mode, officialSum)
		}
	}

	checkMode("source")

	repo.RequireDirectoryProjection = true
	if _, err := s.RebuildDirectory(ctx, p, "school", nil); err != nil {
		t.Fatal(err)
	}
	checkMode("projection")
}
