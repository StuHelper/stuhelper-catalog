package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strings"
)

const TeacherHomepageMaxPhotoBytes = 2 << 20

const (
	teacherHomepageFieldLimit   = 128
	teacherHomepageSectionLimit = 64
)

type TeacherHomepageField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type TeacherHomepageSection struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type TeacherHomepagePhoto struct {
	ID          string `json:"-"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// TeacherHomepage is the matched website's plain-text projection for the
// teacher detail. The academic directory continues to own Object.Biography
// (including the directory title) and Object.DepartmentID.
type TeacherHomepage struct {
	Name           string                   `json:"name"`
	Position       string                   `json:"position,omitempty"`
	Education      string                   `json:"education,omitempty"`
	Degree         string                   `json:"degree,omitempty"`
	Discipline     string                   `json:"discipline,omitempty"`
	Biography      string                   `json:"biography,omitempty"`
	Research       string                   `json:"research,omitempty"`
	Email          string                   `json:"email,omitempty"`
	Phone          string                   `json:"phone,omitempty"`
	OfficeLocation string                   `json:"office_location,omitempty"`
	HireDate       string                   `json:"hire_date,omitempty"`
	Supervisors    []string                 `json:"supervisors"`
	Fields         []TeacherHomepageField   `json:"fields"`
	Sections       []TeacherHomepageSection `json:"sections"`
	Photo          *TeacherHomepagePhoto    `json:"photo,omitempty"`
}

// TeacherHomepageImage identifies a verified blob by its lowercase SHA-256.
// Inline bytes are optional for imports supplying a lazy ReadPhoto callback.
type TeacherHomepageImage struct {
	DeliverySchool string `json:"-"`
	ID             string `json:"id"`
	MediaType      string `json:"content_type"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	SizeBytes      int64  `json:"-"`
	Bytes          []byte `json:"-"`
}

type TeacherSite struct {
	SiteID              string                     `json:"site_id"`
	Name                string                     `json:"name"`
	Unit                string                     `json:"unit"`
	URL                 string                     `json:"url"`
	Reason              string                     `json:"reason"`
	CandidateTeacherIDs []string                   `json:"candidate_teacher_ids"`
	CandidateCount      int64                      `json:"candidate_count"`
	Profile             TeacherHomepage            `json:"profile"`
	Photo               *TeacherHomepageImage      `json:"photo,omitempty"`
	BodyMedia           []TeacherHomepageBodyMedia `json:"body_media,omitempty"`
}

type TeacherSiteTeacher struct {
	TeacherID        string   `json:"teacher_id"`
	Reason           string   `json:"reason"`
	CandidateSiteIDs []string `json:"candidate_site_ids"`
	CandidateCount   int64    `json:"candidate_count"`
}

type TeacherHomepageTransaction interface {
	TeacherHomepage(school, teacherID string) (*TeacherHomepage, error)
	TeacherHomepagePhoto(school, teacherID, photoID string) (TeacherHomepageImage, error)
}

// TeacherSiteID scopes a stable source identifier to its school before it
// enters catalog storage or an administrative response.
func TeacherSiteID(school, sourceID string) string {
	return "site-" + digest([]string{school, sourceID})
}

func validTeacherHomepageHash(id string) bool {
	sum, err := hex.DecodeString(id)
	return err == nil && len(sum) == sha256.Size && hex.EncodeToString(sum) == id
}

func validTeacherSiteID(id string) bool {
	suffix, ok := strings.CutPrefix(id, "site-")
	return ok && validTeacherHomepageHash(suffix)
}

func validTeacherHomepagePhotoMetadata(photo TeacherHomepageImage) bool {
	return validTeacherHomepageHash(photo.ID) &&
		(photo.MediaType == "image/jpeg" || photo.MediaType == "image/png") &&
		validTeacherHomepagePhotoDimensions(photo.Width, photo.Height)
}

// ValidateTeacherHomepageImage checks the actual encoded bytes before the
// repository retains them. DecodeConfig bounds memory before full decoding.
func ValidateTeacherHomepageImage(photo TeacherHomepageImage) error {
	if !validTeacherHomepagePhotoMetadata(photo) || len(photo.Bytes) == 0 || len(photo.Bytes) > TeacherHomepageMaxPhotoBytes {
		return ErrInput
	}
	return validateTeacherHomepageImageBytes(photo.Bytes, photo.ID, photo.MediaType, photo.Width, photo.Height)
}

func validateTeacherHomepageImageBytes(data []byte, id, mediaType string, width, height int) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != id {
		return ErrInput
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != mediaType || cfg.Width != width || cfg.Height != height {
		return ErrInput
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	if err != nil {
		return ErrInput
	}
	return nil
}

func validateTeacherHomepage(profile TeacherHomepage) bool {
	if !validText(profile.Name, 256) || strings.TrimSpace(profile.Name) == "" || profile.Photo != nil {
		return false
	}
	for _, optional := range []string{profile.Position, profile.Education, profile.Degree, profile.Discipline, profile.Email, profile.Phone, profile.OfficeLocation, profile.HireDate} {
		if !validText(optional, 512) || optional != "" && strings.TrimSpace(optional) == "" {
			return false
		}
	}
	for _, optional := range []string{profile.Biography, profile.Research} {
		if !validText(optional, 16000) || optional != "" && strings.TrimSpace(optional) == "" {
			return false
		}
	}
	if len(profile.Supervisors) > 2 || len(profile.Fields) > teacherHomepageFieldLimit || len(profile.Sections) > teacherHomepageSectionLimit {
		return false
	}
	for i, supervisor := range profile.Supervisors {
		if (supervisor != "博士生导师" && supervisor != "硕士生导师") || slices.Contains(profile.Supervisors[:i], supervisor) {
			return false
		}
	}
	for _, field := range profile.Fields {
		if strings.TrimSpace(field.Label) == "" || !validText(field.Label, 160) ||
			strings.TrimSpace(field.Value) == "" || !validText(field.Value, 1024) {
			return false
		}
	}
	for _, section := range profile.Sections {
		if strings.TrimSpace(section.Title) == "" || !validText(section.Title, 160) ||
			strings.TrimSpace(section.Text) == "" || !validText(section.Text, 16000) {
			return false
		}
	}
	raw, err := json.Marshal(profile)
	return err == nil && len(raw) <= 1<<20
}

func normalizeTeacherHomepage(profile *TeacherHomepage) {
	if profile.Fields == nil {
		profile.Fields = []TeacherHomepageField{}
	}
	if profile.Sections == nil {
		profile.Sections = []TeacherHomepageSection{}
	}
	if profile.Supervisors == nil {
		profile.Supervisors = []string{}
	}
}

func normalizeTeacherReviewCandidates(ids []string, count int64, valid func(string) bool) ([]string, bool) {
	if len(ids) > 100 || count < int64(len(ids)) || count > 100000 {
		return nil, false
	}
	out := append([]string{}, ids...)
	slices.Sort(out)
	for i, id := range out {
		if !valid(id) || i > 0 && out[i-1] == id {
			return nil, false
		}
	}
	return out, true
}

func validateTeacherHomepageSnapshot(in *TeacherSiteSnapshot, hosts []string) error {
	if in.Sites == nil && in.Version == 0 {
		if len(in.Teachers) != 0 || in.ReadPhoto != nil || in.ReadBodyImage != nil {
			return ErrInput
		}
		return nil
	}
	if in.Version != 0 && in.Version != 2 && in.Version != 3 || len(in.Sites) > 20000 || len(in.Teachers) > 100000 {
		return ErrInput
	}
	if in.Version == 0 {
		in.Version = 2
	}
	if in.Version < 3 && in.ReadBodyImage != nil {
		return ErrInput
	}
	in.Sites = append([]TeacherSite{}, in.Sites...)
	in.Teachers = append([]TeacherSiteTeacher{}, in.Teachers...)
	slices.SortFunc(in.Sites, func(a, b TeacherSite) int { return strings.Compare(a.SiteID, b.SiteID) })
	slices.SortFunc(in.Teachers, func(a, b TeacherSiteTeacher) int { return strings.Compare(a.TeacherID, b.TeacherID) })
	matchesByURL := make(map[string]string, len(in.Matches))
	matchedTeachers := make(map[string]bool, len(in.Matches))
	for _, match := range in.Matches {
		if matchesByURL[match.URL] != "" {
			return ErrInput
		}
		matchesByURL[match.URL] = match.TeacherID
		matchedTeachers[match.TeacherID] = true
	}
	sites := make(map[string]bool, len(in.Sites))
	urls := make(map[string]bool, len(in.Sites))
	for i := range in.Sites {
		site := &in.Sites[i]
		autoID := matchesByURL[site.URL]
		if !validTeacherSiteID(site.SiteID) || sites[site.SiteID] || urls[site.URL] || !validTeacherSiteURL(site.URL, hosts) ||
			strings.TrimSpace(site.Name) == "" || !validText(site.Name, 256) || !validText(site.Unit, 512) ||
			(site.Reason == "") != (autoID != "") ||
			site.Reason != "" && site.Reason != "site_unavailable" && !slices.Contains(TeacherUnmatchedReasons, site.Reason) {
			return ErrInput
		}
		sites[site.SiteID] = true
		urls[site.URL] = true
		var ok bool
		site.CandidateTeacherIDs, ok = normalizeTeacherReviewCandidates(site.CandidateTeacherIDs, site.CandidateCount, ValidID)
		if !ok {
			return ErrInput
		}
		if site.Reason == "site_unavailable" {
			site.Profile = TeacherHomepage{}
			if site.Photo != nil || len(site.BodyMedia) != 0 {
				return ErrInput
			}
			continue
		}
		normalizeTeacherHomepage(&site.Profile)
		if !validateTeacherHomepage(site.Profile) || site.Profile.Name != site.Name {
			return ErrInput
		}
		if site.Photo != nil && (!validTeacherHomepagePhotoMetadata(*site.Photo) ||
			len(site.Photo.Bytes) == 0 && in.ReadPhoto == nil || len(site.Photo.Bytes) > TeacherHomepageMaxPhotoBytes) {
			return ErrInput
		}
		if err := validateTeacherHomepageBodyMedia(*site, in.Version == 3, hosts); err != nil {
			return err
		}
	}
	for raw := range matchesByURL {
		if !urls[raw] {
			return ErrInput
		}
	}
	teachers := make(map[string]bool, len(in.Teachers))
	for i := range in.Teachers {
		teacher := &in.Teachers[i]
		if !ValidID(teacher.TeacherID) || teachers[teacher.TeacherID] ||
			(teacher.Reason == "") != matchedTeachers[teacher.TeacherID] || !validTeacherReviewReason(teacher.Reason) {
			return ErrInput
		}
		teachers[teacher.TeacherID] = true
		var ok bool
		teacher.CandidateSiteIDs, ok = normalizeTeacherReviewCandidates(teacher.CandidateSiteIDs, teacher.CandidateCount, func(id string) bool { return sites[id] })
		if !ok {
			return ErrInput
		}
	}
	for _, site := range in.Sites {
		for _, id := range site.CandidateTeacherIDs {
			if !teachers[id] {
				return ErrInput
			}
		}
	}
	for id := range matchedTeachers {
		if !teachers[id] {
			return ErrInput
		}
	}
	return nil
}

func validTeacherReviewReason(reason string) bool {
	switch reason {
	case "", "catalog_name_missing", "name_absent", "catalog_unit_missing", "unit_mismatch", "site_unit_missing", "ambiguous_catalog", "ambiguous_site", "site_unavailable":
		return true
	}
	return false
}

func teacherHomepageID(teacher Object) string {
	if teacher.CanonicalID != "" {
		return teacher.CanonicalID
	}
	return teacher.ID
}

func attachTeacherHomepage(t Transaction, teacher *Object) error {
	profile, err := publishedTeacherHomepage(t, *teacher)
	// Legacy clients require the name when the old optional extension exists.
	// The dedicated profile route can return the remaining visible fields.
	if err != nil || profile == nil || profile.Name == "" {
		return err
	}
	maskHomepageContacts(profile)
	teacher.TeacherHomepage = profile
	return nil
}

func (s *Service) ReadTeacherHomepagePhoto(ctx context.Context, p Principal, id, photoID string) (TeacherHomepageImage, error) {
	var out TeacherHomepageImage
	if !validTeacherHomepageHash(photoID) {
		return out, ErrInput
	}
	err := s.readPublicObject(ctx, p, id, true, func(t Transaction, teacher Object) error {
		var err error
		out, err = currentTeacherPhoto(t, teacher, photoID, true)
		return err
	})
	return out, err
}

// TeacherPhotoForDelivery checks the live publication without reading the COS payload.
func (s *Service) TeacherPhotoForDelivery(ctx context.Context, p Principal, id, digest string) (TeacherImageKey, error) {
	var key TeacherImageKey
	if !validTeacherHomepageHash(digest) {
		return key, ErrInput
	}
	err := s.readPublicObject(ctx, p, id, true, func(t Transaction, teacher Object) error {
		key = TeacherImageKey{Kind: SitePortrait, School: teacher.SchoolID, OwnerID: teacherHomepageID(teacher), Digest: digest}
		visible, err := TeacherImageIsPublished(t, teacher, key)
		if err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		return nil
	})
	return key, err
}

func currentTeacherPhoto(t Transaction, teacher Object, photoID string, loadBytes bool) (TeacherHomepageImage, error) {
	port, ok := t.(TeacherHomepageTransaction)
	if !ok || teacher.Kind != "teacher" {
		return TeacherHomepageImage{}, ErrNotFound
	}
	presentation, err := readTeacherHomepagePresentation(t, teacher)
	if err != nil {
		return TeacherHomepageImage{}, err
	}
	show, override := presentation.photo()
	if !show {
		return TeacherHomepageImage{}, ErrNotFound
	}
	canonical := teacherHomepageID(teacher)
	if override != nil {
		if override.ID != photoID {
			return TeacherHomepageImage{}, ErrNotFound
		}
		if !presentation.controls.Independent {
			source, err := port.TeacherHomepage(teacher.SchoolID, canonical)
			if err != nil {
				return TeacherHomepageImage{}, err
			}
			if source == nil {
				return TeacherHomepageImage{}, ErrNotFound
			}
		}
		pictures, ok := t.(TeacherHomepageOverridePhotoTransaction)
		if !ok {
			return TeacherHomepageImage{}, ErrUnavailable
		}
		if !loadBytes {
			return photoMetadata(t, teacher.SchoolID, canonical, photoID, true)
		}
		return pictures.TeacherHomepageOverridePhoto(teacher.SchoolID, canonical, photoID)
	}
	if !loadBytes {
		return photoMetadata(t, teacher.SchoolID, canonical, photoID, false)
	}
	return port.TeacherHomepagePhoto(teacher.SchoolID, canonical, photoID)
}

func photoMetadata(t Transaction, school, teacher, digest string, override bool) (TeacherHomepageImage, error) {
	p, ok := t.(interface {
		TeacherHomepagePhotoMetadata(string, string, string, bool) (TeacherHomepageImage, error)
	})
	if !ok {
		return TeacherHomepageImage{}, ErrUnavailable
	}
	return p.TeacherHomepagePhotoMetadata(school, teacher, digest, override)
}
