package catalog

import "context"

// SchoolDataAccess holds the owner's authorization for the duration of read.
// Implementations serialize revocation with the callback and supply the school
// of the current credential. A portal login alone grants no enrollment access.
type SchoolDataAccess interface {
	WithSchoolData(context.Context, string, func(context.Context, string) error) error
}

func withSchoolData[T any](s *Service, ctx context.Context, p Principal, school string, read func(context.Context, string) (T, error)) (T, error) {
	var zero, result T
	if p.Subject == "" {
		return zero, ErrUnauthenticated
	}
	if s.SchoolDataAccess == nil {
		return zero, ErrForbidden
	}
	err := s.SchoolDataAccess.WithSchoolData(ctx, p.Subject, func(authorized context.Context, granted string) error {
		if !ValidID(granted) || school != "" && granted != school {
			return ErrForbidden
		}
		var err error
		result, err = read(authorized, granted)
		return err
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
