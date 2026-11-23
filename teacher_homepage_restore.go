package catalog

import (
	"context"
	"time"
)

// RestoreTeacherHomepageText attaches a verified historical text snapshot to
// its existing directory membership. The file digest identifies the original
// bytes, including fields that have no publication representation in v1.
func (s *Service) RestoreTeacherHomepageText(ctx context.Context, p Principal, in TeacherSiteSnapshot, fileDigest string, checkedAt map[string]time.Time) error {
	if p.Subject == "" || !p.CanImport || !allowed(p, in.SchoolID) {
		return ErrForbidden
	}
	if len(checkedAt) != len(in.Sites) || !validTeacherHomepageHash(fileDigest) || len(in.Sites) == 0 || in.Version != 2 ||
		in.Revision != in.ObservedAt.UnixMicro() || in.ReadBodyImage != nil || in.ReadPhoto != nil {
		return ErrInput
	}
	for _, site := range in.Sites {
		observed := checkedAt[site.SiteID]
		if observed.IsZero() || observed.After(in.ObservedAt) {
			return ErrInput
		}
		v := site.Profile
		if site.Photo != nil || len(site.BodyMedia) > 0 || v.Photo != nil || v.Email != "" || v.Phone != "" ||
			v.OfficeLocation != "" || v.HireDate != "" || len(v.Fields)+len(v.Sections) > 0 {
			return ErrInput
		}
	}
	if err := s.validateTeacherSites(&in); err != nil {
		return err
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(in.SchoolID); err != nil {
			return err
		}
		port, ok := t.(interface {
			RestoreTeacherHomepageText(TeacherSiteSnapshot, string, map[string]time.Time, string, time.Time) error
		})
		if !ok {
			return ErrUnavailable
		}
		return port.RestoreTeacherHomepageText(in, fileDigest, checkedAt, p.Subject, s.now())
	})
}
