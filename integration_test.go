package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
)

func fixture(t *testing.T) (*c.Service, c.Principal, c.Import) {
	t.Helper()
	repo, _ := database(t)
	s := &c.Service{Repo: repo}
	p := c.Principal{Subject: "issuer:admin", Schools: []string{"school"}, CanImport: true, CanMaintain: true}
	object := func(id, kind string) c.Object {
		return c.Object{ID: id, Kind: kind, SchoolID: "school", Name: id, Aliases: []string{}}
	}
	school := object("school", "school")
	dept := object("dept", "department")
	category := object("seminar", "category")
	category.Name = "新生研讨课"
	nature := object("optional", "nature")
	term := object("term1", "term")
	term2 := object("term2", "term")
	course := object("course", "course")
	course.DepartmentID = "dept"
	teacher := object("teacher", "teacher")
	teacher2 := object("teacher2", "teacher")
	tc := object("offering1", "term_course")
	tc.CourseID = "course"
	tc.TermID = "term1"
	tc2 := object("offering2", "term_course")
	tc2.CourseID = "course"
	tc2.TermID = "term2"
	class := object("class1", "class")
	class.TermCourseID = "offering1"
	class.CategoryID = "seminar"
	class.NatureID = "optional"
	class2 := object("class2", "class")
	class2.TermCourseID = "offering1"
	class2.CategoryID = "seminar"
	a := object("assignment", "assignment")
	a.ClassID = "class1"
	a.TeacherID = "teacher"
	a2 := object("assignment2", "assignment")
	a2.ClassID = "class1"
	a2.TeacherID = "teacher2"
	in := c.Import{SchoolID: "school", SourceID: "approved-source", Revision: 1, FetchedAt: time.Now().UTC(), Objects: []c.Object{class, tc, teacher, a, school, dept, term, course, category, nature, teacher2, a2, term2, tc2, class2}, Enrollments: []c.Enrollment{{ID: "enrollment", Subject: "issuer:student", SchoolID: "school", CourseID: "course", TermID: "term1", ClassID: "class1", TeacherIDs: []string{"teacher", "teacher2"}, Active: true}}}
	return s, p, in
}
func TestImportRelationsClassificationAndMissingCredits(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, e := s.Import(ctx, p, "import", in); e != nil {
		t.Fatal(e)
	}
	page, e := s.List(ctx, c.Filter{Kind: "course", SchoolID: "school", CategoryID: "seminar", DepartmentID: "dept", Limit: 20})
	if e != nil || len(page.Items) != 1 {
		t.Fatalf("classification: %+v %v", page, e)
	}
	if page.Items[0].Credits != nil {
		t.Fatal("unknown credits became zero")
	}
	classes, e := s.List(ctx, c.Filter{Kind: "class", CourseID: "course", TermID: "term1", Limit: 20})
	if e != nil || len(classes.Items) != 2 {
		t.Fatalf("teaching classes: %+v %v", classes, e)
	}
	data, e := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", "course", "term1", "", 20)
	if e != nil || len(data) != 1 || len(data[0].TeacherIDs) != 2 {
		t.Fatalf("enrollment: %+v %v", data, e)
	}
	other, e := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:other"}, "school", "course", "term1", "", 20)
	if e != nil || len(other) != 0 {
		t.Fatal("other user's evidence leaked")
	}
	next, e := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", "course", "term2", "", 20)
	if e != nil || len(next) != 0 {
		t.Fatal("another term granted evidence")
	}
}

func TestCourseTeacherJoinsAndEnrollmentAssignments(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	teachers, err := s.List(ctx, c.Filter{Kind: "teacher", CourseID: "course", TermID: "term1", Limit: 20})
	if err != nil || len(teachers.Items) != 2 {
		t.Fatalf("course teachers: %+v %v", teachers, err)
	}
	teachers, err = s.List(ctx, c.Filter{Kind: "teacher", CourseID: "course", TermID: "term2", Limit: 20})
	if err != nil || len(teachers.Items) != 0 {
		t.Fatal("teacher from another term leaked")
	}
	in.Revision = 2
	in.FetchedAt = time.Now().UTC()
	in.Objects = nil
	in.Enrollments[0].ClassID = "class2"
	if _, err = s.Import(ctx, p, "wrong-class", in); !errors.Is(err, c.ErrInput) {
		t.Fatal("teacher not assigned to student's class was accepted", err)
	}
	own, err := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", "course", "term1", "", 20)
	if err != nil || len(own) != 1 || own[0].ClassID != "class1" {
		t.Fatal("rejected import changed enrollment")
	}
}
func TestImportIdempotencyConcurrencyAndRollback(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() { _, e := s.Import(ctx, p, "same", in); results <- e })
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	changed := in
	changed.Revision = 2
	if _, e := s.Import(ctx, p, "same", changed); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("key reuse: %v", e)
	}
	bad := c.Import{SchoolID: "school", SourceID: "approved-source", Revision: 2, FetchedAt: time.Now().UTC(), Objects: []c.Object{{ID: "fresh", Kind: "teacher", SchoolID: "school", Name: "New"}, {ID: "bad", Kind: "assignment", SchoolID: "school", Name: "Broken", ClassID: "missing", TeacherID: "fresh"}}}
	if _, e := s.Import(ctx, p, "bad", bad); e == nil {
		t.Fatal("broken relation accepted")
	}
	if _, e := s.Get(ctx, "fresh"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("partial import persisted")
	}
	page, e := s.List(ctx, c.Filter{Kind: "course", Limit: 20})
	if e != nil || len(page.Items) != 1 {
		t.Fatal("failed import erased prior course")
	}
}
func TestCorrectionsSurviveImportsAndStaleSourceIsRejected(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, e := s.Import(ctx, p, "first", in); e != nil {
		t.Fatal(e)
	}
	name := "人工确认的名称"
	corrected, e := s.Correct(ctx, p, "course", "edit", c.Correction{Revision: 1, Name: &name})
	if e != nil || corrected.Revision != 2 {
		t.Fatal(e)
	}
	update := in
	update.Revision = 2
	update.Objects = append([]c.Object(nil), in.Objects...)
	update.Objects[7].Name = "Imported new name"
	update.FetchedAt = time.Now().UTC()
	if _, e = s.Import(ctx, p, "second", update); e != nil {
		t.Fatal(e)
	}
	course, e := s.Get(ctx, "course")
	if e != nil || course.Name != name {
		t.Fatalf("manual field lost: %+v %v", course, e)
	}
	if _, e = s.Import(ctx, p, "old", in); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("stale source: %v", e)
	}
	if _, e = s.Correct(ctx, p, "course", "stale", c.Correction{Revision: 1, Name: &name}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("stale correction accepted")
	}
}
func TestSchoolScopePublicProjectionAndBoundedHTTP(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	wrong := p
	wrong.Schools = []string{"other"}
	if _, e := s.Import(ctx, wrong, "forbidden", in); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("cross-school import allowed")
	}
	if _, e := s.Import(ctx, p, "ok", in); e != nil {
		t.Fatal(e)
	}
	h := httpapi.Handler{Service: s}.Routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/catalog/objects?kind=course", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "issuer:student") || strings.Contains(w.Body.String(), "source_id") {
		t.Fatal("public provenance leaked")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/catalog/objects?kind=course&limit=101", nil))
	if w.Code != 400 {
		t.Fatal("unbounded request allowed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/catalog/me/enrollments?subject=issuer:student", nil))
	if w.Code != 503 {
		t.Fatal("missing identity must fail closed")
	}
	var got c.Page
	if e := json.Unmarshal([]byte(`{"items":[],"next":""}`), &got); e != nil {
		t.Fatal(e)
	}
}
