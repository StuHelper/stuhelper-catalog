package catalog

import (
	"context"
	"errors"
	"time"
)

type DiscoveryTarget struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
}

// DiscoveryDocument matches the search discovery event schema. Ratings are zero
// because catalog does not own rating facts. Invisible documents are tombstones.
type DiscoveryDocument struct {
	Code     string          `json:"code,omitempty"`
	Aliases  []string        `json:"aliases,omitempty"`
	Target   DiscoveryTarget `json:"target"`
	Revision int64           `json:"revision"`
	Title    string          `json:"title"`
	SchoolID string          `json:"school_id"`
	Visible  bool            `json:"visible"`
	Ratings  [5]int          `json:"ratings"`
	At       time.Time       `json:"at"`
}
type DiscoveryPage struct {
	Items []DiscoveryDocument
	Next  string
}

// DiscoverySource is a trusted backend rebuild port, not a client route.
type DiscoverySource interface {
	DiscoverySnapshot(context.Context, string, int) (DiscoveryPage, error)
}

// DiscoveryCurrent rechecks current admission, school and canonical visibility.
// Only exact canonical IDs are returned; old aliases must not revive stale hits.
func (s *Service) DiscoveryCurrent(ctx context.Context, p Principal, ids []string) ([]DiscoveryDocument, error) {
	out := []DiscoveryDocument{}
	if len(ids) < 1 || len(ids) > 100 {
		return nil, ErrInput
	}
	for _, id := range ids {
		if !ValidID(id) {
			return nil, ErrInput
		}
	}
	school, e := s.readScope(ctx, p, "")
	if errors.Is(e, ErrForbidden) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	e = s.Repo.Transaction(ctx, func(t Transaction) error { var e error; out, e = t.DiscoveryCurrent(school, ids); return e })
	return out, e
}
