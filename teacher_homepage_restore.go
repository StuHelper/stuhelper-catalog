package catalog

import (
	"context"
	"slices"
	"strings"
	"time"
)

// LegacyHomepageReview identifies one old automatic association that the
// current complete roster can no longer resolve uniquely.
type LegacyHomepageReview struct {
	SiteID       string `json:"site_id"`
	TeacherID    string `json:"teacher_id"`
	DepartmentID string `json:"department_id"`
	Reason       string `json:"reason"`
}

// RestoreTeacherHomepageText attaches a verified historical text snapshot to
// its existing directory membership. The file digest identifies the original
// bytes, including fields that have no publication representation in v1.
func (s *Service) RestoreTeacherHomepageText(ctx context.Context, p Principal, in TeacherSiteSnapshot, fileDigest string, checkedAt map[string]time.Time) error {
	return s.RestoreTeacherHomepageTextWithReview(ctx, p, in, fileDigest, checkedAt, nil)
}

func (s *Service) RestoreTeacherHomepageTextWithReview(ctx context.Context, p Principal, in TeacherSiteSnapshot, fileDigest string, checkedAt map[string]time.Time, review []LegacyHomepageReview) error {
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
	if len(review) > len(in.Sites) {
		return ErrInput
	}
	review = append([]LegacyHomepageReview{}, review...)
	slices.SortFunc(review, func(a, b LegacyHomepageReview) int { return strings.Compare(a.SiteID, b.SiteID) })
	teachers := map[string]bool{}
	for i, row := range review {
		if !validTeacherSiteID(row.SiteID) || !ValidID(row.TeacherID) || !ValidID(row.DepartmentID) ||
			row.Reason != "ambiguous_catalog" && row.Reason != "ambiguous_site" || teachers[row.TeacherID] || i > 0 && review[i-1].SiteID == row.SiteID {
			return ErrInput
		}
		teachers[row.TeacherID] = true
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(in.SchoolID); err != nil {
			return err
		}
		port, ok := t.(interface {
			RestoreTeacherHomepageText(TeacherSiteSnapshot, string, map[string]time.Time, []LegacyHomepageReview, string, time.Time) error
		})
		if !ok {
			return ErrUnavailable
		}
		return port.RestoreTeacherHomepageText(in, fileDigest, checkedAt, review, p.Subject, s.now())
	})
}
