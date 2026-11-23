package catalog

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

type HomepageScopeSite struct {
	SiteID    string `json:"site_id"`
	URL       string `json:"url"`
	Name      string `json:"name"`
	TeacherID string `json:"teacher_id,omitempty"`
}

type HomepageDetailScope struct {
	Format         string              `json:"format"`
	SchoolID       string              `json:"school_id"`
	SourceRevision int64               `json:"source_revision"`
	ReviewRevision int64               `json:"review_revision"`
	Total          int                 `json:"total"`
	Sites          []HomepageScopeSite `json:"sites"`
}

// Set contains only values observed on the homepage. Missing scalar values
// keep their previous observation. Dynamic items supplement richer sections.
type HomepageDetailPatch struct {
	SchoolID       string                     `json:"school_id"`
	SourceRevision int64                      `json:"source_revision"`
	ReviewRevision int64                      `json:"review_revision"`
	SiteID         string                     `json:"site_id"`
	TeacherID      string                     `json:"teacher_id"`
	URL            string                     `json:"url"`
	CheckedAt      time.Time                  `json:"checked_at"`
	PageStatus     string                     `json:"page_status"`
	Set            map[string]json.RawMessage `json:"set,omitempty"`
	Photo          *TeacherHomepageImage      `json:"photo,omitempty"`
}

type homepagePartialPort interface {
	HomepageDetailScope(string) (HomepageDetailScope, error)
	ApplyHomepageDetailPatch(HomepageDetailPatch, string, time.Time) error
}

func (s *Service) HomepageDetailScope(ctx context.Context, p Principal, school string) (HomepageDetailScope, error) {
	var out HomepageDetailScope
	if p.Subject == "" || !p.CanImport || !allowed(p, school) {
		return out, ErrForbidden
	}
	if !ValidID(school) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		port, ok := t.(homepagePartialPort)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.HomepageDetailScope(school)
		return err
	})
	return out, err
}

func (s *Service) ApplyHomepageDetailPatch(ctx context.Context, p Principal, in HomepageDetailPatch) error {
	if p.Subject == "" || !p.CanImport || !allowed(p, in.SchoolID) {
		return ErrForbidden
	}
	if !ValidID(in.SchoolID) || !ValidID(in.TeacherID) || !validTeacherSiteID(in.SiteID) || !validTeacherSiteURL(in.URL, s.allowedTeacherSiteHosts(in.SchoolID)) ||
		in.ReviewRevision < 1 || in.SourceRevision < 1 || in.CheckedAt.UnixMicro() <= in.SourceRevision || in.CheckedAt.After(s.now().Add(5*time.Minute)) {
		return ErrInput
	}
	if in.PageStatus != "available" && (in.PageStatus != "not_found" || len(in.Set) > 0 || in.Photo != nil) {
		return ErrInput
	}
	for key, raw := range in.Set {
		if !slices.Contains(teacherHomepageScalarFields[1:], key) && key != "sections" && key != "fields" {
			return ErrInput
		}
		if string(raw) == "null" || !json.Valid(raw) {
			return ErrInput
		}
	}
	if in.Photo != nil {
		if !validTeacherHomepagePhotoMetadata(*in.Photo) {
			return ErrInput
		}
		if err := ValidateTeacherHomepageImage(*in.Photo); err != nil {
			return err
		}
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(in.SchoolID); err != nil {
			return err
		}
		port, ok := t.(homepagePartialPort)
		if !ok {
			return ErrUnavailable
		}
		return port.ApplyHomepageDetailPatch(in, p.Subject, s.now())
	})
}

// MergeHomepageDetail combines text while retaining sections known only from a
// previous full collection. New observations cannot change identity or media.
func MergeHomepageDetail(old TeacherHomepage, set map[string]json.RawMessage) (TeacherHomepage, error) {
	for key, raw := range set {
		switch key {
		case "sections":
			var items []TeacherHomepageSection
			if json.Unmarshal(raw, &items) != nil {
				return old, ErrInput
			}
			for _, item := range items {
				if !slices.Contains(old.Sections, item) {
					old.Sections = append(old.Sections, item)
				}
			}
		case "fields":
			var items []TeacherHomepageField
			if json.Unmarshal(raw, &items) != nil {
				return old, ErrInput
			}
			for _, item := range items {
				if !slices.Contains(old.Fields, item) {
					old.Fields = append(old.Fields, item)
				}
			}
		default:
			if !slices.Contains(teacherHomepageScalarFields[1:], key) {
				return old, ErrInput
			}
			v := homepageScalar(&old, key)
			if json.Unmarshal(raw, v) != nil || *v == "" {
				return old, ErrInput
			}
		}
	}
	normalizeTeacherHomepage(&old)
	if !validateTeacherHomepage(old) {
		return old, ErrInput
	}
	return old, nil
}
