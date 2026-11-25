package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"

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
