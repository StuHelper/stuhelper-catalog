package catalog

import (
	"context"
	"time"
)

// AdvanceConfiguredProjection commits at most one stage of the configured
// school's build. An unpublished generation is resumable across workers.
// A zero version indicates that a stale draft was discarded.
func (s *Service) AdvanceConfiguredProjection(ctx context.Context, p Principal, school string, policy DirectoryPolicy) (version int64, step int, done bool, err error) {
	if p.Subject == "" {
		return 0, 0, false, ErrUnauthenticated
	}
	if !p.CanMaintain || !allowed(p, school) {
		return 0, 0, false, ErrForbidden
	}
	if !ValidID(school) || !policy.Valid() {
		return 0, 0, false, ErrInput
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err = s.Repo.Transaction(call, func(tx Transaction) error {
		port, ok := tx.(interface {
			AdvanceConfiguredProjection(string, DirectoryPolicy, time.Time, string) (int64, int, bool, error)
		})
		if !ok {
			return ErrUnavailable
		}
		var e error
		version, step, done, e = port.AdvanceConfiguredProjection(school, policy, s.now(), p.Subject)
		return e
	})
	return
}

// ConfiguredProjectionCurrent checks the published policy and format, including
// any unfinished work to supersede, without updating source terms.
func (s *Service) ConfiguredProjectionCurrent(ctx context.Context, p Principal, school string, policy DirectoryPolicy) (current bool, err error) {
	if p.Subject == "" {
		return false, ErrUnauthenticated
	}
	if !p.CanMaintain || !allowed(p, school) {
		return false, ErrForbidden
	}
	if !ValidID(school) || !policy.Valid() {
		return false, ErrInput
	}
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(interface {
			ConfiguredProjectionCurrent(string, DirectoryPolicy) (bool, error)
		})
		if !ok {
			return ErrUnavailable
		}
		var e error
		current, e = port.ConfiguredProjectionCurrent(school, policy)
		return e
	})
	return
}
