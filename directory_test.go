package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
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
	h := httpapi.Handler{Service: s}.Routes()
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
