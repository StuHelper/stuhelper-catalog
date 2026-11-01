package catalog

import (
	"context"
	"slices"
)

type Reference struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	CanonicalID string `json:"canonical_id,omitempty"`
}
type References struct {
	Items      []Reference `json:"items"`
	MissingIDs []string    `json:"missing_ids"`
}

func referenceIDs(ids []string) ([]string, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return nil, ErrInput
	}
	ids = slices.Clone(ids)
	for _, id := range ids {
		if !ValidID(id) {
			return nil, ErrInput
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
func referenceNames(tx Transaction, ids []string) (References, error) {
	out := References{Items: []Reference{}, MissingIDs: []string{}}
	items, e := tx.ReferenceNames(ids)
	if e != nil {
		return out, e
	}
	out.Items = items
	found := map[string]bool{}
	for _, item := range items {
		found[item.ID] = true
	}
	for _, id := range ids {
		if !found[id] {
			out.MissingIDs = append(out.MissingIDs, id)
		}
	}
	return out, nil
}

// ReferenceNames is an internal module port. A caller must first authorize the
// owning record; HTTP callers only reach MyReferences or the content read APIs.
func (s *Service) ReferenceNames(ctx context.Context, ids []string) (References, error) {
	ids, e := referenceIDs(ids)
	if e != nil {
		return References{}, e
	}
	var out References
	e = s.Repo.Transaction(ctx, func(tx Transaction) error { var e error; out, e = referenceNames(tx, ids); return e })
	return out, e
}
func (s *Service) MyReferences(ctx context.Context, p Principal, ids []string) (References, error) {
	if p.Subject == "" {
		return References{}, ErrUnauthenticated
	}
	ids, e := referenceIDs(ids)
	if e != nil {
		return References{}, e
	}
	var out References
	e = s.Repo.Transaction(ctx, func(tx Transaction) error {
		owned, e := tx.OwnedReferences(p.Subject, ids)
		if e != nil {
			return e
		}
		if len(owned) != len(ids) {
			return ErrNotFound
		}
		out, e = referenceNames(tx, ids)
		return e
	})
	return out, e
}
