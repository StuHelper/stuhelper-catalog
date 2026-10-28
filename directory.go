package catalog

import (
	"context"
	"slices"
)

func (s *Service) Lookup(ctx context.Context, ids []string) (Lookup, error) {
	out := Lookup{Items: []Object{}, MissingIDs: []string{}}
	if len(ids) < 1 || len(ids) > 100 {
		return out, ErrInput
	}
	ids = slices.Clone(ids)
	for _, id := range ids {
		if !ValidID(id) {
			return out, ErrInput
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		out.Items, err = t.Lookup(ids)
		if err != nil {
			return err
		}
		found := make(map[string]bool, len(out.Items))
		for _, item := range out.Items {
			found[item.ID] = true
		}
		for _, id := range ids {
			if !found[id] {
				out.MissingIDs = append(out.MissingIDs, id)
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) Summary(ctx context.Context, id string) (Summary, error) {
	var out Summary
	if !ValidID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		object, err := t.PublicObject(id)
		if err != nil {
			return err
		}
		if object.Kind != "course" && object.Kind != "teacher" {
			return ErrInput
		}
		out, err = t.Summary(object)
		return err
	})
	return out, err
}
