package catalog

import (
	"context"
	"slices"
	"time"
)

type portraitExclusionPort interface {
	ExcludeSitePortrait(school, digest string, sites []string, actor string, at time.Time) error
}

// ExcludeSitePortrait withdraws one source image only if the caller names every
// site using it in this school. Personal overrides and body images are separate.
func (s *Service) ExcludeSitePortrait(ctx context.Context, p Principal, school, digest string, sites []string) error {
	if p.Subject == "" || !p.CanMaintain || !allowed(p, school) {
		return ErrForbidden
	}
	if !ValidID(school) || !validTeacherHomepageHash(digest) || len(sites) == 0 || len(sites) > 100 {
		return ErrInput
	}
	sites = slices.Clone(sites)
	slices.Sort(sites)
	for i, site := range sites {
		if !validTeacherSiteID(site) || i > 0 && sites[i-1] == site {
			return ErrInput
		}
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		store, ok := t.(portraitExclusionPort)
		if !ok {
			return ErrUnavailable
		}
		return store.ExcludeSitePortrait(school, digest, sites, p.Subject, s.now())
	})
}
