package catalog

import "context"

type FilterOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type FilterOptions struct {
	Kind        string         `json:"kind"`
	Total       int64          `json:"total"`
	Departments []FilterOption `json:"departments"`
	Categories  []FilterOption `json:"categories"`
	Natures     []FilterOption `json:"natures"`
}

type FilterOptionsTransaction interface {
	FilterOptions(kind, school string) (FilterOptions, error)
}

// ReadFilterOptions describes the complete visible directory. Selection-specific
// facets stay on ReadDirectory, so picking a filter cannot remove other choices.
func (s *Service) ReadFilterOptions(ctx context.Context, p Principal, kind, school string) (FilterOptions, error) {
	if teachingDetail(kind) {
		return FilterOptions{}, ErrForbidden
	}
	if kind != "course" && kind != "teacher" {
		return FilterOptions{}, ErrInput
	}
	school, err := s.readScope(ctx, p, school)
	if err != nil {
		return FilterOptions{}, err
	}
	var out FilterOptions
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(FilterOptionsTransaction)
		if !ok {
			return ErrUnavailable
		}
		out, err = port.FilterOptions(kind, school)
		return err
	})
	return out, err
}
