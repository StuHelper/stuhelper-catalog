package catalog

import "context"

// ReviewEnrollmentTransaction resolves a projected target from the requester's
// original enrollment. The source course, term and class must all agree.
type ReviewEnrollmentTransaction interface {
	ReviewEnrollments(subject, school, course, term, after string, limit int) ([]Enrollment, error)
}

func (s *Service) ReviewEnrollments(ctx context.Context, p Principal, course, term, after string, limit int) ([]Enrollment, error) {
	if p.Subject == "" {
		return nil, ErrUnauthenticated
	}
	if !ValidID(course) || (term != "" && !ValidID(term)) || (after != "" && !ValidID(after)) || limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	if err := s.refreshOwn(ctx, p); err != nil {
		return nil, err
	}
	var out []Enrollment
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		target, err := t.PublicObject(course)
		if err != nil {
			return err
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
