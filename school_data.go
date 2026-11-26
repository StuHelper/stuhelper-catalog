package catalog

import (
	"context"
	"errors"
)

type SchoolDataScope struct {
	SchoolID            string
	ThroughTermOrderKey int
}

// SchoolDataAccess holds the owner's authorization for the duration of read.
// Implementations serialize changes with the callback and supply the school
// and its inclusive term limit.
type SchoolDataAccess interface {
	WithSchoolData(context.Context, string, func(context.Context, SchoolDataScope) error) error
}

type schoolDataScopeKey struct{}

func enrollmentTermLimit(ctx context.Context) int {
	through, _ := ctx.Value(schoolDataScopeKey{}).(int)
	return through
}

func withSchoolData[T any](s *Service, ctx context.Context, p Principal, school, term string, read func(context.Context, string) (T, error)) (T, error) {
	var zero, result T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if p.Subject == "" {
		return zero, ErrUnauthenticated
	}
	if s.SchoolDataAccess == nil {
		return zero, ErrForbidden
	}
	err := s.SchoolDataAccess.WithSchoolData(ctx, p.Subject, func(authorized context.Context, scope SchoolDataScope) error {
		if !ValidID(scope.SchoolID) || school != "" && scope.SchoolID != school || scope.ThroughTermOrderKey < 19001 ||
			scope.ThroughTermOrderKey > 22003 || scope.ThroughTermOrderKey%10 < 1 || scope.ThroughTermOrderKey%10 > 3 {
			return ErrForbidden
		}
		if term != "" {
			if !ValidID(term) {
				return ErrInput
			}
			if err := s.Repo.Transaction(authorized, func(tx Transaction) error {
				order, err := tx.SchoolTermOrder(scope.SchoolID, term)
				if errors.Is(err, ErrNotFound) || err == nil && (order <= 0 || order > scope.ThroughTermOrderKey) {
					return ErrForbidden
				}
				return err
			}); err != nil {
				return err
			}
		}
		var err error
		authorized = context.WithValue(authorized, schoolDataScopeKey{}, scope.ThroughTermOrderKey)
		result, err = read(authorized, scope.SchoolID)
		return err
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
