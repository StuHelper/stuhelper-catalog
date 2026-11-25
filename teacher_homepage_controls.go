package catalog

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	TeacherHomepageVisible = "visible"
	TeacherHomepageHidden  = "hidden"
	TeacherHomepageStopped = "stopped"
)

var ErrTeacherHomepageCapacity = errors.New("teacher_homepage_capacity_exceeded")

type TeacherHomepageFieldSelector struct {
	Field string `json:"field"`
	Label string `json:"label,omitempty"`
}

type TeacherHomepageFieldValue struct {
	Text        *string                    `json:"text,omitempty"`
	Supervisors *[]string                  `json:"supervisors,omitempty"`
	Fields      *[]TeacherHomepageField    `json:"fields,omitempty"`
	Sections    *[]TeacherHomepageSection  `json:"sections,omitempty"`
	Photo       *TeacherHomepageFieldPhoto `json:"photo,omitempty"`
}

type TeacherHomepageFieldPhoto struct {
	ID          string `json:"id,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type TeacherHomepageFieldRule struct {
	Field    TeacherHomepageFieldSelector `json:"field"`
	Hidden   bool                         `json:"hidden"`
	Override *TeacherHomepageFieldValue   `json:"override,omitempty"`
}

type TeacherHomepageControls struct {
	Revision       int64                      `json:"revision"`
	PageVisibility string                     `json:"page_visibility"`
	Rules          []TeacherHomepageFieldRule `json:"rules"`
	Independent    bool                       `json:"independent"`
}

type TeacherHomepageControlsInput struct {
	ExpectedRevision *int64                     `json:"expected_revision"`
	PageVisibility   string                     `json:"page_visibility"`
	Rules            []TeacherHomepageFieldRule `json:"rules"`
}

type TeacherHomepageControlsView struct {
	Source   *TeacherHomepage        `json:"source,omitempty"`
	Controls TeacherHomepageControls `json:"controls"`
}

type TeacherHomepageControlsReceipt struct {
	TeacherID string `json:"teacher_id"`
	Revision  int64  `json:"revision"`
}

// TeacherHomepageControlsTransaction persists independent state keyed by the
// school and directory teacher. The caller holds the school lock for writes.
type TeacherHomepageControlsTransaction interface {
	TeacherHomepageControls(school, teacherID string) (TeacherHomepageControls, error)
	SaveTeacherHomepageControls(school, teacherID string, controls TeacherHomepageControls, now time.Time) error
}

type TeacherHomepageOverridePhotoTransaction interface {
	TeacherHomepageOverridePhoto(school, teacherID, photoID string) (TeacherHomepageImage, error)
	SaveTeacherHomepageOverridePhoto(school, teacherID string, photo TeacherHomepageImage) error
	DeleteTeacherHomepageOverridePhoto(school, teacherID string) error
}

func teacherHomepageLabelKey(label string) string {
	label = stripHomepageContactMarker(label)
	return cases.Fold().String(strings.Join(strings.Fields(strings.TrimRight(norm.NFKC.String(strings.TrimSpace(label)), ":")), " "))
}

func teacherHomepageSelectorKey(field TeacherHomepageFieldSelector) TeacherHomepageFieldSelector {
	return TeacherHomepageFieldSelector{Field: field.Field, Label: teacherHomepageLabelKey(field.Label)}
}

func teacherHomepageValueType(field TeacherHomepageFieldSelector) (string, int) {
	switch field.Field {
	case "name":
		return "text", 256
	case "position", "education", "degree", "discipline", "email", "phone", "office_location", "hire_date":
		return "text", 512
	case "biography", "research":
		return "text", 16000
	case "supervisors":
		return "supervisors", 2
	case "fields":
		if field.Label != "" {
			return "text", 1024
		}
		return "fields", teacherHomepageFieldLimit
	case "sections":
		if field.Label != "" {
			return "text", 16000
		}
		return "sections", teacherHomepageSectionLimit
	case "photo":
		return "photo", 0
	default:
		return "", 0
	}
}

func normalizeTeacherHomepageRule(rule TeacherHomepageFieldRule) (TeacherHomepageFieldRule, error) {
	if rule.Field.Label != "" {
		if rule.Field.Field != "fields" && rule.Field.Field != "sections" ||
			!validTeacherHomepageLabel(rule.Field.Label) {
			return rule, ErrInput
		}
		rule.Field.Label = strings.TrimSpace(rule.Field.Label)
	}
	kind, limit := teacherHomepageValueType(rule.Field)
	if kind == "" || !rule.Hidden && rule.Override == nil {
		return rule, ErrInput
	}
	if rule.Override == nil {
		return rule, nil
	}
	if !validTeacherHomepageOverride(*rule.Override, kind, limit) {
		return rule, ErrInput
	}
	return rule, nil
}

func validTeacherHomepageOverride(value TeacherHomepageFieldValue, kind string, limit int) bool {
	switch kind {
	case "text":
		return value.Supervisors == nil && value.Fields == nil && value.Sections == nil && value.Photo == nil &&
			value.Text != nil && validHomepageOverrideText(*value.Text, limit)
	case "supervisors":
		if value.Text != nil || value.Fields != nil || value.Sections != nil || value.Photo != nil ||
			value.Supervisors == nil || *value.Supervisors == nil || len(*value.Supervisors) > limit {
			return false
		}
		for i, item := range *value.Supervisors {
			if (item != "博士生导师" && item != "硕士生导师") || slices.Contains((*value.Supervisors)[:i], item) {
				return false
			}
		}
	case "fields":
		if value.Text != nil || value.Supervisors != nil || value.Sections != nil || value.Photo != nil ||
			value.Fields == nil || *value.Fields == nil || len(*value.Fields) > limit {
			return false
		}
		for _, field := range *value.Fields {
			if !validTeacherHomepageLabel(field.Label) ||
				!validHomepageOverrideText(field.Value, 1024) {
				return false
			}
		}
	case "sections":
		if value.Text != nil || value.Supervisors != nil || value.Fields != nil || value.Photo != nil ||
			value.Sections == nil || *value.Sections == nil || len(*value.Sections) > limit {
			return false
		}
		for _, section := range *value.Sections {
			if !validTeacherHomepageLabel(section.Title) ||
				!validHomepageOverrideText(section.Text, 16000) {
				return false
			}
		}
	case "photo":
		return value.Text == nil && value.Supervisors == nil && value.Fields == nil && value.Sections == nil &&
			value.Photo != nil && (validTeacherHomepagePhotoInput(*value.Photo) || validStoredTeacherHomepagePhoto(*value.Photo))
	default:
		return false
	}
	return true
}

func validStoredTeacherHomepagePhoto(photo TeacherHomepageFieldPhoto) bool {
	return photo.Data == nil && validTeacherHomepagePhotoMetadata(TeacherHomepageImage{
		ID: photo.ID, MediaType: photo.ContentType, Width: photo.Width, Height: photo.Height,
	})
}

func validTeacherHomepagePhotoInput(photo TeacherHomepageFieldPhoto) bool {
	if photo.Width != 0 || photo.Height != 0 {
		return false
	}
	if photo.ID != "" {
		return photo.ContentType == "" && photo.Data == nil && validTeacherHomepageHash(photo.ID)
	}
	return (photo.ContentType == "image/jpeg" || photo.ContentType == "image/png") &&
		len(photo.Data) > 0 && len(photo.Data) <= TeacherHomepageMaxPhotoBytes
}

func validHomepageOverrideText(text string, limit int) bool {
	return validText(text, limit) && strings.TrimSpace(text) != ""
}

func normalizeTeacherHomepageControls(in TeacherHomepageControls) (TeacherHomepageControls, error) {
	if (in.PageVisibility != TeacherHomepageVisible && in.PageVisibility != TeacherHomepageHidden && in.PageVisibility != TeacherHomepageStopped) ||
		in.Rules == nil || len(in.Rules) > 256 {
		return in, ErrInput
	}
	rules := make([]TeacherHomepageFieldRule, 0, len(in.Rules))
	seen := make(map[TeacherHomepageFieldSelector]bool, len(in.Rules))
	for _, item := range in.Rules {
		rule, err := normalizeTeacherHomepageRule(item)
		if err != nil {
			return in, err
		}
		key := teacherHomepageSelectorKey(rule.Field)
		if seen[key] {
			return in, ErrInput
		}
		seen[key] = true
		rules = append(rules, rule)
	}
	in.Rules = rules
	// Photo bytes travel in the upload. The separate photo table stores the
	// binary data and the rule contains the validated address and dimensions.
	bounded := in
	bounded.Revision = math.MaxInt64
	bounded.Rules = slices.Clone(in.Rules)
	for i, rule := range bounded.Rules {
		if rule.Override == nil || rule.Override.Photo == nil {
			continue
		}
		value := *rule.Override
		value.Photo = &TeacherHomepageFieldPhoto{ID: strings.Repeat("0", 64), ContentType: "image/jpeg", Width: 4096, Height: 4096}
		bounded.Rules[i].Override = &value
	}
	raw, err := json.Marshal(bounded)
	if err != nil || len(raw) > 1<<20 {
		return in, ErrInput
	}
	return in, nil
}

func (s *Service) ReadTeacherHomepageControls(ctx context.Context, p Principal, school, teacher string) (TeacherHomepageControlsView, error) {
	var result TeacherHomepageControlsView
	const access = "homepage-controls-viewed"
	if err := s.teacherRefreshAccess(ctx, p, school, access, p.CanMaintain); err != nil {
		return result, err
	}
	if !ValidID(teacher) {
		return result, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		controls, controlsOK := t.(TeacherHomepageControlsTransaction)
		homepage, homepageOK := t.(TeacherHomepageTransaction)
		refresh, err := refreshPort(t)
		if err != nil || !controlsOK || !homepageOK {
			return ErrUnavailable
		}
		if err := refresh.BeginTeacherRefreshSnapshot(); err != nil {
			return err
		}
		result.Controls, err = controls.TeacherHomepageControls(school, teacher)
		if err != nil {
			return err
		}
		result.Source, err = homepage.TeacherHomepage(school, teacher)
		if err != nil {
			return err
		}
		// A raw object does not have an independently authorized picture URL.
		if result.Source != nil {
			result.Source.Photo = nil
			normalizeTeacherHomepage(result.Source)
		}
		return s.teacherRefreshAudit(t, p, school, access, "teacher/"+teacher, "success")
	})
	return result, err
}

func (s *Service) SetTeacherHomepageControls(ctx context.Context, p Principal, school, teacher, idempotencyKey string, in TeacherHomepageControlsInput) (TeacherHomepageControlsReceipt, error) {
	var result TeacherHomepageControlsReceipt
	if err := s.teacherRefreshAccess(ctx, p, school, "homepage-controls-updated", p.CanMaintain); err != nil {
		return result, err
	}
	if !ValidID(teacher) || !ValidKey(idempotencyKey) || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 ||
		in.Rules == nil || len(in.Rules) > 256 {
		return result, ErrInput
	}
	for _, rule := range in.Rules {
		if rule.Override != nil && rule.Override.Photo != nil && !validTeacherHomepagePhotoInput(*rule.Override.Photo) {
			return result, ErrInput
		}
	}
	desired := TeacherHomepageControls{PageVisibility: in.PageVisibility, Rules: in.Rules}
	replayKey := "teacher-homepage-controls:" + digest([]string{p.Subject, school, idempotencyKey})
	fingerprint := digest([]any{teacher, in})
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		cached, err := t.Replay(replayKey)
		if err == nil {
			if cached.Digest != fingerprint {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &result)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		updated, err := s.applyTeacherHomepageControls(t, p, school, teacher, *in.ExpectedRevision, desired, s.now())
		if err != nil {
			return err
		}
		result = TeacherHomepageControlsReceipt{TeacherID: teacher, Revision: updated.Revision}
		// A later correction or removal must not be recoverable through an
		// older idempotency key. The durable receipt carries no profile data.
		data, err := json.Marshal(result)
		if err != nil {
			return ErrUnavailable
		}
		return t.SaveReplay(replayKey, Replay{Digest: fingerprint, Result: data})
	})
	return result, err
}

// applyTeacherHomepageControls is shared by direct edits and request approval.
// The caller holds the school lock and commits the edit and audit atomically.
func (s *Service) applyTeacherHomepageControls(t Transaction, p Principal, school, teacher string, expectedRevision int64, desired TeacherHomepageControls, now time.Time) (TeacherHomepageControls, error) {
	return s.applyTeacherHomepageControlsWithPhoto(t, p, school, teacher, expectedRevision, desired, now, nil)
}

func (s *Service) applyTeacherHomepageControlsWithPhoto(t Transaction, p Principal, school, teacher string, expectedRevision int64, desired TeacherHomepageControls, now time.Time, approved *TeacherHomepageImage) (TeacherHomepageControls, error) {
	var result TeacherHomepageControls
	if !p.CanMaintain || !allowed(p, school) {
		return result, ErrForbidden
	}
	controls, ok := t.(TeacherHomepageControlsTransaction)
	refresh, err := refreshPort(t)
	if err != nil || !ok {
		return result, ErrUnavailable
	}
	result, err = normalizeTeacherHomepageControls(desired)
	if err != nil {
		return result, err
	}
	current, err := controls.TeacherHomepageControls(school, teacher)
	if err != nil {
		return result, err
	}
	if current.Revision != expectedRevision || expectedRevision == math.MaxInt64 {
		return result, ErrConflict
	}
	if err := validateTeacherHomepageOverrideCapacity(result); err != nil {
		return result, err
	}
	result.Revision = current.Revision + 1
	result.Independent = current.Independent
	if err := applyTeacherHomepageControlPhoto(t, school, teacher, current, &result, approved); err != nil {
		return result, err
	}
	if err := controls.SaveTeacherHomepageControls(school, teacher, result, now); err != nil {
		return result, err
	}
	changes := homepageControlsAuditChanges(school, teacher, current, result)
	err = refresh.RecordTeacherRefreshAudit(Event{
		ID: "homepage-controls:" + rand.Text(), Subject: "teacher/" + teacher,
		Type: teacherHomepageControlsAudit, SchoolID: school, Actor: p.Subject,
		Revision: result.Revision, Result: "success", Time: now,
		Reason: changes.reason(homepageControlsAuditReasons), HomepageChanges: &changes,
	})
	return result, err
}

func teacherHomepagePhotoRule(controls TeacherHomepageControls) (TeacherHomepageFieldRule, bool) {
	for _, rule := range controls.Rules {
		if rule.Field.Field == "photo" {
			return rule, true
		}
	}
	return TeacherHomepageFieldRule{}, false
}

func applyTeacherHomepageControlPhoto(t Transaction, school, teacher string, current TeacherHomepageControls, desired *TeacherHomepageControls, approved *TeacherHomepageImage) error {
	index := slices.IndexFunc(desired.Rules, func(rule TeacherHomepageFieldRule) bool { return rule.Field.Field == "photo" })
	var provided *TeacherHomepageFieldPhoto
	if index >= 0 && desired.Rules[index].Override != nil {
		provided = desired.Rules[index].Override.Photo
	}
	previous, _ := teacherHomepagePhotoRule(current)
	if provided == nil && previous.Override == nil {
		return nil
	}
	port, ok := t.(TeacherHomepageOverridePhotoTransaction)
	if !ok {
		return ErrUnavailable
	}
	if provided == nil {
		return port.DeleteTeacherHomepageOverridePhoto(school, teacher)
	}
	var picture TeacherHomepageImage
	switch {
	case approved != nil:
		if provided.ID != approved.ID || !validTeacherHomepagePhotoMetadata(*approved) || len(approved.Bytes) == 0 ||
			len(approved.Bytes) > TeacherHomepageMaxPhotoBytes {
			return ErrInput
		}
		picture = *approved
	case provided.ID != "":
		if previous.Override == nil || previous.Override.Photo == nil || previous.Override.Photo.ID != provided.ID {
			return ErrInput
		}
		desired.Rules[index].Override = &TeacherHomepageFieldValue{Photo: previous.Override.Photo}
		return nil
	default:
		photo, err := ValidateTeacherHomepagePhotoUpload(provided.ContentType, provided.Data)
		if err != nil {
			return err
		}
		picture = photo
	}
	if err := port.SaveTeacherHomepageOverridePhoto(school, teacher, picture); err != nil {
		return err
	}
	desired.Rules[index].Override = &TeacherHomepageFieldValue{Photo: &TeacherHomepageFieldPhoto{
		ID: picture.ID, ContentType: picture.MediaType, Width: picture.Width, Height: picture.Height,
	}}
	return nil
}
