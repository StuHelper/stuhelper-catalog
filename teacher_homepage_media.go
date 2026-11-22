package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	TeacherHomepageMaxBodyImageBytes = 20 << 20
	teacherHomepageBodyMediaLimit    = 1000
	teacherHomepageBodyImageEdge     = 8192
	teacherHomepageBodyImagePixels   = 16_777_216
	teacherHomepageBodyLinkBytes     = 2048
)

// TeacherHomepageBodyMedia keeps the verified source link even when an image
// copy is available so an import can fall back if its bytes cannot be stored.
type TeacherHomepageBodyMedia struct {
	Kind    string                    `json:"kind"`
	Field   string                    `json:"field"`
	Section string                    `json:"section,omitempty"`
	Title   string                    `json:"title,omitempty"`
	LinkURL string                    `json:"link_url"`
	Image   *TeacherHomepageBodyImage `json:"image,omitempty"`
}

type TeacherHomepageBodyImage struct {
	ID        string `json:"id"`
	MediaType string `json:"content_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"size_bytes"`
}

type TeacherHomepageBodyMediaTransaction interface {
	TeacherHomepageBodyMedia(school, teacherID string) ([]TeacherHomepageBodyMedia, error)
	TeacherHomepageBodyImage(school, teacherID, imageID string, loadBytes bool) (TeacherHomepageImage, error)
}

type TeacherHomepageMedium struct {
	Kind    string                         `json:"kind"`
	Field   string                         `json:"field"`
	Section string                         `json:"section,omitempty"`
	Title   string                         `json:"title,omitempty"`
	URL     string                         `json:"url"`
	Image   *TeacherHomepagePublishedImage `json:"image,omitempty"`
}

type TeacherHomepagePublishedImage struct {
	MediaType string `json:"content_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"size_bytes"`
}

type TeacherHomepageMediaView struct {
	Items []TeacherHomepageMedium `json:"items"`
}

func validTeacherHomepageBodyImage(image TeacherHomepageBodyImage) bool {
	return validTeacherHomepageHash(image.ID) && (image.MediaType == "image/jpeg" || image.MediaType == "image/png") &&
		image.SizeBytes > 0 && image.SizeBytes <= TeacherHomepageMaxBodyImageBytes &&
		image.Width > 0 && image.Width <= teacherHomepageBodyImageEdge && image.Height > 0 && image.Height <= teacherHomepageBodyImageEdge &&
		image.Width*image.Height <= teacherHomepageBodyImagePixels
}

// ValidateTeacherHomepageBodyImage checks re-encoded bytes before storage.
func ValidateTeacherHomepageBodyImage(image TeacherHomepageBodyImage, data []byte) error {
	if !validTeacherHomepageBodyImage(image) || int64(len(data)) != image.SizeBytes {
		return ErrInput
	}
	return validateTeacherHomepageImageBytes(data, image.ID, image.MediaType, image.Width, image.Height)
}

var (
	teacherHomepageBodyPath   = regexp.MustCompile(`^(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]+\.htm$`)
	teacherHomepageHostLabel  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	teacherHomepageWebPort    = map[string]string{"http": "80", "https": "443"}
	teacherHomepageHostSuffix = []string{"localhost", "local", "lan", "internal", "home", "test", "invalid", "onion", "example"}
)

func teacherHomepageWebURL(raw string) (*url.URL, bool) {
	if len(raw) == 0 || len(raw) > teacherHomepageBodyLinkBytes || strings.ContainsFunc(raw, func(r rune) bool {
		return r <= 32 || r >= 127 || r == '\\'
	}) {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	port, ok := teacherHomepageWebPort[u.Scheme]
	if !ok || !teacherHomepageWebHostname(u.Hostname()) ||
		u.Host != u.Hostname() && u.Host != u.Hostname()+":"+port {
		return nil, false
	}
	query, err := url.QueryUnescape(u.RawQuery)
	if err != nil || !safeTeacherHomepageURLText(u.Path) || !safeTeacherHomepageURLText(query) || !safeTeacherHomepageURLText(u.Fragment) {
		return nil, false
	}
	return u, true
}

func teacherHomepageWebHostname(host string) bool {
	labels := strings.Split(strings.ToLower(host), ".")
	if len(host) > 253 || len(labels) < 2 {
		return false
	}
	suffix := labels[len(labels)-1]
	if suffix == "" || suffix[0] < 'a' || suffix[0] > 'z' || slices.Contains(teacherHomepageHostSuffix, suffix) {
		return false
	}
	return !slices.ContainsFunc(labels, func(label string) bool { return !teacherHomepageHostLabel.MatchString(label) })
}

func safeTeacherHomepageURLText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\\'
	})
}

func teacherHomepageSourcePage(raw string, link, home *url.URL) bool {
	if link.Scheme != "https" || !strings.EqualFold(link.Host, home.Host) || link.RawQuery != "" || link.ForceQuery || strings.Contains(raw, "#") ||
		strings.Count(link.EscapedPath(), "/") != strings.Count(link.Path, "/") {
		return false
	}
	contentPath, ok := strings.CutPrefix(link.Path, path.Dir(home.Path)+"/")
	return ok && teacherHomepageBodyPath.MatchString(contentPath)
}

func validTeacherHomepageMediaLabel(value string) bool {
	return validText(value, 160) && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029'
	})
}

func validateTeacherHomepageBodyMedia(site TeacherSite, versionThree bool, hosts []string) error {
	if len(site.BodyMedia) == 0 {
		return nil
	}
	if !versionThree || len(site.BodyMedia) > teacherHomepageBodyMediaLimit || !validTeacherSiteURL(site.URL, hosts) {
		return ErrInput
	}
	home, _ := url.Parse(site.URL)
	for _, item := range site.BodyMedia {
		if (item.Field != "biography" && item.Field != "research" && item.Field != "sections") ||
			(item.Field == "sections") != (item.Section != "") || !validTeacherHomepageMediaLabel(item.Section) || !validTeacherHomepageMediaLabel(item.Title) {
			return ErrInput
		}
		link, valid := teacherHomepageWebURL(item.LinkURL)
		if !valid {
			return ErrInput
		}
		switch item.Kind {
		case "image":
			if !teacherHomepageSourcePage(item.LinkURL, link, home) || item.Image != nil && !validTeacherHomepageBodyImage(*item.Image) {
				return ErrInput
			}
		case "video", "attachment":
			if item.Image != nil {
				return ErrInput
			}
		default:
			return ErrInput
		}
	}
	// A source record is capped before Catalog adds school-scoped IDs and
	// candidate metadata. Bound the new payload on its own here.
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(site.BodyMedia); err != nil || raw.Len() > 2<<20 {
		return ErrInput
	}
	return nil
}

func (p homepagePresentation) mediaArea(item TeacherHomepageBodyMedia, redaction *homepageRedaction) (string, bool) {
	group := TeacherHomepageFieldSelector{Field: item.Field}
	if p.hidden[group.Field] || p.rules[group].Override != nil {
		return "", false
	}
	if item.Field != "sections" {
		return "", p.allowed[group]
	}
	key := teacherHomepageSelectorKey(TeacherHomepageFieldSelector{Field: group.Field, Label: item.Section})
	if rule := p.rules[key]; rule.Override == nil {
		visible, ok := p.dynamicItem(key, homepageTextItem{label: item.Section, text: item.Section}, rule, false, redaction)
		return visible.label, ok
	}
	return "", false
}

func (p homepagePresentation) renderMedium(item TeacherHomepageBodyMedia, teacherID string, redaction *homepageRedaction) (TeacherHomepageMedium, bool) {
	section, ok := p.mediaArea(item, redaction)
	if !ok || item.Title != "" && redaction.clean(item.Title, false) != item.Title {
		return TeacherHomepageMedium{}, false
	}
	out := TeacherHomepageMedium{Kind: item.Kind, Field: item.Field, Section: section, Title: item.Title}
	if image := item.Image; image != nil {
		out.URL = "/api/v1/catalog/objects/" + url.PathEscape(teacherID) + "/teacher-homepage/images/" + image.ID
		out.Image = &TeacherHomepagePublishedImage{MediaType: image.MediaType, Width: image.Width, Height: image.Height, SizeBytes: image.SizeBytes}
	} else {
		link, ok := redaction.mediaLink(item.LinkURL)
		if !ok {
			return TeacherHomepageMedium{}, false
		}
		out.URL = link
	}
	return out, true
}

func (r *homepageRedaction) mediaLink(raw string) (string, bool) {
	u, valid := teacherHomepageWebURL(raw)
	if !valid {
		return "", false
	}
	query, _ := url.QueryUnescape(u.RawQuery)
	for _, value := range []string{u.Path, query, u.Fragment} {
		normalized := teacherHomepageLabelKey(value)
		if r.contains(normalized, false) || (r.hidden["email"] || r.excluded["email"]) && homepageEmail.MatchString(normalized) {
			return "", false
		}
	}
	return u.String(), true
}

func publishedTeacherHomepageMedia(t Transaction, teacher Object, imageID string) ([]TeacherHomepageMedium, error) {
	presentation, err := readTeacherHomepagePresentation(t, teacher)
	if err != nil || !presentation.permitsPage() {
		return nil, err
	}
	port, ok := t.(TeacherHomepageBodyMediaTransaction)
	if !ok {
		return nil, ErrUnavailable
	}
	id := teacherHomepageID(teacher)
	media, err := port.TeacherHomepageBodyMedia(teacher.SchoolID, id)
	if err != nil || len(media) == 0 {
		return nil, err
	}
	profilePort, ok := t.(TeacherHomepageTransaction)
	if !ok {
		return nil, ErrUnavailable
	}
	source, err := profilePort.TeacherHomepage(teacher.SchoolID, id)
	if err != nil || source == nil {
		return nil, err
	}
	redaction := presentation.redaction(*source)
	var visible []TeacherHomepageMedium
	for _, item := range media {
		if imageID != "" && (item.Image == nil || item.Image.ID != imageID) {
			continue
		}
		if out, ok := presentation.renderMedium(item, id, &redaction); ok {
			visible = append(visible, out)
			if imageID != "" {
				break
			}
		}
	}
	return visible, nil
}

func (s *Service) ReadTeacherHomepageMedia(ctx context.Context, p Principal, id string) (TeacherHomepageMediaView, error) {
	out := TeacherHomepageMediaView{Items: []TeacherHomepageMedium{}}
	err := s.readPublicObject(ctx, p, id, true, func(t Transaction, teacher Object) error {
		if teacher.Kind != "teacher" {
			return ErrNotFound
		}
		visible, err := publishedTeacherHomepageMedia(t, teacher, "")
		if len(visible) != 0 {
			out.Items = visible
		}
		return err
	})
	return out, err
}

func (s *Service) ReadTeacherHomepageBodyImage(ctx context.Context, p Principal, id, imageID string, loadBytes bool) (TeacherHomepageImage, error) {
	var out TeacherHomepageImage
	if !validTeacherHomepageHash(imageID) {
		return out, ErrInput
	}
	err := s.readPublicObject(ctx, p, id, true, func(t Transaction, teacher Object) error {
		if teacher.Kind != "teacher" {
			return ErrNotFound
		}
		visible, err := publishedTeacherHomepageMedia(t, teacher, imageID)
		if err != nil {
			return err
		}
		if len(visible) == 0 {
			return ErrNotFound
		}
		// The approved-media read above used this same transaction and repository.
		port := t.(TeacherHomepageBodyMediaTransaction)
		out, err = port.TeacherHomepageBodyImage(teacher.SchoolID, teacherHomepageID(teacher), imageID, loadBytes)
		out.DeliverySchool = teacher.SchoolID
		if err != nil {
			return err
		}
		image := visible[0].Image
		if image == nil || out.ID != imageID || out.MediaType != image.MediaType || out.Width != image.Width || out.Height != image.Height ||
			out.SizeBytes != image.SizeBytes || loadBytes && int64(len(out.Bytes)) != image.SizeBytes {
			return ErrUnavailable
		}
		return nil
	})
	return out, err
}
