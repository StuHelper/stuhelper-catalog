package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

type countedEnrollmentSource struct{ reads int }

func (s *countedEnrollmentSource) OwnSnapshot(context.Context, string) (c.EnrollmentSnapshot, error) {
	s.reads++
	return c.EnrollmentSnapshot{}, c.ErrUnavailable
}
func TestMissingSchoolDataAuthorizationSkipsPersonalSources(t *testing.T) {
	ctx := context.Background()
	p := c.Principal{Subject: "synthetic-owner"}
	source := new(countedEnrollmentSource)
	s := c.Service{EnrollmentSource: source}
	for _, read := range []func() error{
		func() error { _, err := s.MyEnrollments(ctx, p, "school", "course", "term", "", 10); return err },
		func() error { _, err := s.MySchedule(ctx, p, "term", "", 10); return err },
		func() error { _, err := s.MyReferences(ctx, p, []string{"course"}); return err },
		func() error { _, err := s.ReviewEnrollments(ctx, p, "course", "term", "", 10); return err },
	} {
		if err := read(); !errors.Is(err, c.ErrForbidden) {
			t.Errorf("read returned %v", err)
		}
	}
	if source.reads != 0 {
		t.Errorf("unauthorized calls to enrollment source: %d", source.reads)
	}
}

func TestPersonalEnrollmentQueriesRespectSchoolDataTermLimit(t *testing.T) {
	s, admin, seed := fixture(t)
	ctx := context.Background()
	owner := c.Principal{Subject: seed.Enrollments[0].Subject}
	future := seed.Enrollments[0]
	future.ID, future.TermID, future.ClassID, future.TeacherIDs = "future", "term2", "", nil
	seed.Enrollments = append(seed.Enrollments, future)
	if _, err := s.Import(ctx, admin, "scoped", seed); err != nil {
		t.Fatal(err)
	}
	s.SchoolDataAccess = fixtureSchoolData{through: 20263}
	items, err := s.MyEnrollments(ctx, owner, "school", "", "", "", 100)
	if err != nil || len(items) != 1 || items[0].ID != "enrollment" {
		t.Fatal("personal list contained a later term", err)
	}
	page, err := s.MySchedule(ctx, owner, "", "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Enrollment.TermID != "term1" {
		t.Fatal("schedule contained a later term", err)
	}
	if _, err := s.MyReferences(ctx, owner, []string{"term2"}); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("personal references exposed a later term", err)
	}
	source := new(countedEnrollmentSource)
	s.EnrollmentSource = source
	for _, read := range []func() error{
		func() error { _, err := s.MyEnrollments(ctx, owner, "school", "", "term2", "", 20); return err },
		func() error { _, err := s.MySchedule(ctx, owner, "term2", "", 20); return err },
		func() error { _, err := s.MyCalendar(ctx, owner, "term2"); return err },
		func() error { _, err := s.ReviewEnrollmentSnapshot(ctx, owner, "course", "term2"); return err },
	} {
		if err := read(); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("out-of-term request: %v", err)
		}
	}
	if source.reads != 0 {
		t.Fatal("out-of-term request read the personal source")
	}
	if err := s.DeleteOwnerEnrollments(ctx, owner.Subject); err != nil {
		t.Fatal(err)
	}
	s.EnrollmentSource = nil
	items, err = s.MyEnrollments(ctx, owner, "school", "", "", "", 100)
	if err != nil || len(items) != 0 {
		t.Fatal("owner enrollment projection survived closure", err)
	}

	empty := c.EnrollmentSnapshot{
		Subject: owner.Subject, SchoolID: "school", SourceID: "replica", Version: "empty",
		Revision: 1, FetchedAt: time.Now().UTC(),
	}
	first, err := s.ReplaceOwnEnrollments(ctx, admin, empty)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteOwnerEnrollments(ctx, owner.Subject, c.OwnedEnrollmentSource{SchoolID: empty.SchoolID, SourceID: empty.SourceID}); err != nil {
		t.Fatal(err)
	}
	after, err := s.ReplaceOwnEnrollments(ctx, admin, empty)
	if err != nil || after.ID == first.ID {
		t.Fatal("empty enrollment snapshot retained its replay after closure", err)
	}
}

func TestPublicTeacherFallbackUsesOnlyTheRequestedCourseAndTerm(t *testing.T) {
	s, admin, seed := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, admin, "course-teacher-fixture", seed); err != nil {
		t.Fatal(err)
	}
	ids, err := s.CourseTermTeachers(ctx, "course", "term1")
	if err != nil || !slices.Equal(ids, []string{"teacher", "teacher2"}) {
		t.Fatal(ids, err)
	}
	ids, err = s.CourseTermTeachers(ctx, "course", "term2")
	if err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
}
