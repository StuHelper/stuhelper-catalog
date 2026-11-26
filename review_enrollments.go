package catalog

import (
	"cmp"
	"context"
)

// ReviewEnrollmentTransaction resolves a projected target from the requester's
// original enrollment. The source course, term and class must all agree.
type ReviewEnrollmentTransaction interface {
	ReviewEnrollments(subject, school, course, term, after string, limit, through int) ([]Enrollment, error)
}

func (s *Service) ReviewEnrollments(ctx context.Context, p Principal, course, term, after string, limit int) ([]Enrollment, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	return withSchoolData(s, ctx, p, "", term, func(ctx context.Context, school string) ([]Enrollment, error) {
		return s.readReviewEnrollments(ctx, p, school, course, term, after, limit)
	})
}

// ReviewEnrollmentSnapshot reads the entire matching set from one refresh and
// one enrollment query. A missing source or an oversized result is inconclusive.
func (s *Service) ReviewEnrollmentSnapshot(ctx context.Context, p Principal, course, term string) ([]Enrollment, error) {
	return withSchoolData(s, ctx, p, "", term, func(ctx context.Context, school string) ([]Enrollment, error) {
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
			out, err = t.Enrollments(p.Subject, target.SchoolID, course, term, after, limit, enrollmentTermLimit(ctx))
			return err
		}
		port, ok := t.(ReviewEnrollmentTransaction)
		if !ok {
			return ErrUnavailable
		}
		out, err = port.ReviewEnrollments(p.Subject, target.SchoolID, target.ID, term, after, limit, enrollmentTermLimit(ctx))
		return err
	})
	return out, err
}

const maxReviewCandidates = 1000

// CourseOfferingTerms returns the current public offering terms in directory order.
func (s *Service) CourseOfferingTerms(ctx context.Context, course string) ([]Object, error) {
	if !ValidID(course) {
		return nil, ErrInput
	}
	var terms []Object
	err := s.Repo.Transaction(ctx, func(tx Transaction) error {
		target, err := tx.PublicObject(course)
		if err != nil {
			return err
		}
		if target.Kind != "course" || target.Inactive {
			return ErrNotFound
		}
		terms, err = tx.List(Filter{Kind: "term", SchoolID: target.SchoolID,
			CourseID: cmp.Or(target.CanonicalID, target.ID), Limit: maxReviewCandidates})
		return err
	})
	if len(terms) > maxReviewCandidates {
		return nil, ErrUnavailable
	}
	return terms, err
}

// CourseTermTeachers reads current public teachers for a visible offering term.
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
		if target.Kind != "course" || period.Kind != "term" || target.SchoolID != period.SchoolID || target.Inactive || period.Inactive {
			return ErrInput
		}
		course = cmp.Or(target.CanonicalID, target.ID)
		term = cmp.Or(period.CanonicalID, period.ID)
		terms, err := tx.List(Filter{Kind: "term", SchoolID: target.SchoolID, CourseID: course, IDs: []string{term}, Limit: 1})
		if err != nil {
			return err
		}
		if len(terms) == 0 {
			return ErrInput
		}
		items, err := tx.List(Filter{Kind: "teacher", SchoolID: target.SchoolID, CourseID: course, TermID: term, Limit: maxReviewCandidates})
		if err != nil {
			return err
		}
		if len(items) > maxReviewCandidates {
			return ErrUnavailable
		}
		ids = make([]string, 0, len(items))
		for _, teacher := range items {
			ids = append(ids, teacher.ID)
		}
		return nil
	})
	return ids, err
}
