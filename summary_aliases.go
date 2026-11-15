package catalog

import (
	"context"
	"slices"
)

type SummaryAliasTransaction interface {
	SummaryAliases(string, string) (map[string]string, error)
}

// SummaryAliases is an in-process metadata port for owning modules to aggregate
// immutable references after a catalog merge. It exposes no personal evidence.
func (s *Service) SummaryAliases(ctx context.Context, school, kind string) (map[string]string, error) {
	if !ValidID(school) || !slices.Contains([]string{"course", "teacher", "term"}, kind) {
		return nil, ErrInput
	}
	var out map[string]string
	err := s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(SummaryAliasTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.SummaryAliases(school, kind)
		return err
	})
	return out, err
}
