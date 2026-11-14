package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
