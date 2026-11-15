package catalog

import "context"

// HasTaught is a trusted composition port for teacher disclosure. The supplied
// teacher IDs come from verified identity. Historical teaching remains evidence
// even when the public directory policy stops listing its term or course.
func (s *Service) HasTaught(ctx context.Context, teachers []string, course string) (bool, error) {
	if !ValidID(course) || len(teachers) > 100 {
		return false, ErrInput
	}
	for _, id := range teachers {
		if !ValidID(id) {
			return false, ErrInput
		}
	}
	if len(teachers) == 0 {
		return false, nil
	}
	var out bool
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(interface {
			HasTaught([]string, string) (bool, error)
		})
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.HasTaught(teachers, course)
		return err
	})
	return out, err
}
