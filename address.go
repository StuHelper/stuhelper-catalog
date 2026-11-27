package catalog

import (
	"context"
	"errors"
	"strings"
)

// ReadAddress uses the published directory for the final object, including
// merged IDs, pending revocations and projection-only sports courses.
func (s *Service) ReadAddress(ctx context.Context, p Principal, kind, address string) (Resolution, error) {
	school, err := s.readScope(ctx, p, "")
	if err != nil {
		return Resolution{}, err
	}
	if (kind != "course" && kind != "teacher") || len(address) == 0 || len(address) > 140 || strings.ContainsAny(address, "/\\") {
		return Resolution{}, ErrInput
	}
	var out Resolution
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		id, err := t.AddressObjectID(school, kind, address)
		if errors.Is(err, ErrNotFound) && ValidID(address) {
			id = address
		} else if err != nil {
			return err
		}
		obj, err := t.PublicObject(id)
		if err != nil {
			return err
		}
		if obj.Kind != kind || obj.Inactive || obj.SchoolID != school {
			return ErrNotFound
		}
		if obj.AddressID == "" {
			return ErrUnavailable
		}
		ids, err := t.ResolveIDs(id)
		if err != nil {
			return err
		}
		out = Resolution{Object: obj, IDs: ids}
		return nil
	})
	return out, err
}

// VisibleAddresses returns only currently readable targets of the requested kind.
// A historical favorite can outlive school membership or the catalog object.
func (s *Service) VisibleAddresses(ctx context.Context, p Principal, kind string, ids []string) (map[string]string, error) {
	school, err := s.readScope(ctx, p, "")
	if err != nil {
		return nil, err
	}
	if kind != "course" && kind != "teacher" || len(ids) > 100 {
		return nil, ErrInput
	}
	result := map[string]string{}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if ValidID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return result, nil
	}
	rows, err := s.Lookup(ctx, valid)
	if err != nil {
		return nil, err
	}
	for _, obj := range rows.Items {
		if obj.Kind == kind && obj.SchoolID == school && !obj.Inactive {
			if obj.AddressID == "" {
				return nil, ErrUnavailable
			}
			result[obj.ID] = obj.AddressID
		}
	}
	return result, nil
}
