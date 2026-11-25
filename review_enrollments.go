package catalog

import "context"

// ReviewEnrollmentTransaction resolves a projected target from the requester's
// original enrollment. The source course, term and class must all agree.
type ReviewEnrollmentTransaction interface {
	ReviewEnrollments(subject, school, course, term, after string, limit int) ([]Enrollment, error)
}

func (s *Service) ReviewEnrollments(ctx context.Context, p Principal, course, term, after string, limit int) ([]Enrollment, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	return withSchoolData(s, ctx, p, "", func(ctx context.Context, school string) ([]Enrollment, error) {
		return s.readReviewEnrollments(ctx, p, school, course, term, after, limit)
	})
}

// ReviewEnrollmentSnapshot reads the entire matching set from one refresh and
// one enrollment query. A missing source or an oversized result is inconclusive.
func (s *Service) ReviewEnrollmentSnapshot(ctx context.Context, p Principal, course, term string) ([]Enrollment, error) {
	return withSchoolData(s, ctx, p, "", func(ctx context.Context, school string) ([]Enrollment, error) {
		if s.EnrollmentSource == nil {
			return nil, ErrUnavailable
		}
		rows, err := s.readReviewEnrollments(ctx, p, school, course, term, "", 1001)
		if len(rows) > 1000 {
			return nil, ErrUnavailable
		}
		return rows, err
	})
}
func (s *Service) readReviewEnrollments(ctx context.Context, p Principal, school string, course, term, after string, limit int) ([]Enrollment, error) {
	if p.Subject == "" {
		return nil, ErrUnauthenticated
	}
	if !ValidID(course) || (term != "" && !ValidID(term)) || (after != "" && !ValidID(after)) {
		return nil, ErrInput
	}
	if err := s.refreshOwn(ctx, p, school); err != nil {
		return nil, err
	}
	var out []Enrollment
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		target, err := t.PublicObject(course)
		if err != nil {
			return err
		}
		if target.SchoolID != school {
			return ErrForbidden
		}
		if target.Kind != "course" {
			return ErrNotFound
		}
		if target.CourseType != "sports_project" {
			out, err = t.Enrollments(p.Subject, target.SchoolID, course, term, after, limit)
			return err
		}
		port, ok := t.(ReviewEnrollmentTransaction)
		if !ok {
			return ErrUnavailable
		}
		out, err = port.ReviewEnrollments(p.Subject, target.SchoolID, target.ID, term, after, limit)
		return err
	})
	return out, err
}

// CourseTermTeachers reads only current public teaching relationships.
func (s *Service) CourseTermTeachers(ctx context.Context, course, term string) ([]string, error) {
	if !ValidID(course) || !ValidID(term) {
		return nil, ErrInput
	}
	var ids []string
	err := s.Repo.Transaction(ctx, func(tx Transaction) error {
		target, e := tx.PublicObject(course)
		if e != nil {
			return e
		}
		period, e := tx.PublicObject(term)
		if e != nil {
			return e
		}
		if target.Kind != "course" || period.Kind != "term" || target.SchoolID != period.SchoolID {
			return ErrInput
		}
		port, ok := tx.(interface {
			CourseTermTeachers(string, string, string) ([]string, error)
		})
		if !ok {
			return ErrUnavailable
		}
		ids, e = port.CourseTermTeachers(target.SchoolID, target.ID, period.ID)
		return e
	})
	return ids, err
}
