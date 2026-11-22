package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const TeacherHomepageRequestResolved = "catalog.teacher-homepage-request.resolved.v1"
const teacherHomepageRequestSubmissionLimit = 10

// TeacherHomepageOwnership verifies the authenticated subject's current
// teacher certification and its binding to the canonical directory object.
type TeacherHomepageOwnership interface {
	OwnsTeacher(ctx context.Context, subject, school, canonicalTeacherID string) (bool, error)
}

type TeacherHomepageRequestInput struct {
	Kind    string                       `json:"kind"`
	Field   TeacherHomepageFieldSelector `json:"field"`
	Value   *TeacherHomepageFieldValue   `json:"value,omitempty"`
	Message string                       `json:"message"`
}

type TeacherHomepageRequest struct {
	TeacherHomepageRequestInput
	ID             string     `json:"id"`
	SchoolID       string     `json:"-"`
	TeacherID      string     `json:"teacher_id"`
	Submitter      string     `json:"-"`
	Status         string     `json:"status"`
	Revision       int64      `json:"revision"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	ResolutionNote string     `json:"resolution_note,omitempty"`
}

type TeacherHomepageRequestDecisionInput struct {
	Action                   string `json:"action"`
	ExpectedRevision         *int64 `json:"expected_revision"`
	ExpectedHomepageRevision *int64 `json:"expected_homepage_revision,omitempty"`
	Note                     string `json:"note"`
}

type TeacherHomepageRequestQuery struct {
	SchoolID, TeacherID, Submitter, Status, After string
	AfterCreatedAt                                time.Time
	Limit                                         int
}

type TeacherHomepageRequestPage struct {
	Items []TeacherHomepageRequest `json:"items"`
	Total int64                    `json:"total"`
	Next  string                   `json:"next"`
}

// TeacherHomepageRequestRef carries only the facts used to resolve signed-in
// notification targets. Proposal and decision contents stay in the queue.
type TeacherHomepageRequestRef struct {
	ID        string `json:"id"`
	SchoolID  string `json:"school_id"`
	TeacherID string `json:"teacher_id"`
	Submitter string `json:"submitter"`
	Status    string `json:"status"`
	Revision  int64  `json:"revision"`
}

type TeacherHomepageRequestTransaction interface {
	InsertTeacherHomepageRequest(TeacherHomepageRequest) error
	GetTeacherHomepageRequest(school, id string) (TeacherHomepageRequest, error)
	ListTeacherHomepageRequests(TeacherHomepageRequestQuery) ([]TeacherHomepageRequest, int64, bool, error)
	ResolveTeacherHomepageRequest(TeacherHomepageRequest) error
	FindTeacherHomepageRequest(id string) (TeacherHomepageRequestRef, error)
	ReadTeacherHomepageRequestsForOwner(owner string, ids []string) ([]TeacherHomepageRequestRef, error)
	ReadTeacherHomepageRequestForOwner(owner, id string) (TeacherHomepageRequest, error)
	TeacherHomepageRequestsSince(submitter string, since time.Time, limit int) (int, error)
	LinkTeacherHomepageRequestReplay(requestID, replayKey string) error
	SaveTeacherHomepageRequestPhoto(school, requestID string, photo TeacherHomepageImage) error
	ReadTeacherHomepageRequestPhoto(school, requestID string) (TeacherHomepageImage, error)
}

func homepageRequestPort(t Transaction) (TeacherHomepageRequestTransaction, error) {
	port, ok := t.(TeacherHomepageRequestTransaction)
	if !ok {
		return nil, ErrUnavailable
	}
	return port, nil
}

func requestTeacher(t Transaction, id string) (Object, error) {
	teacher, err := t.PublicObject(id)
	if err != nil {
		return Object{}, err
	}
	if teacher.Kind != "teacher" || teacher.Inactive {
		return Object{}, ErrNotFound
	}
	if teacher.CanonicalID != "" {
		teacher.ID = teacher.CanonicalID
	}
	return teacher, nil
}

func (s *Service) authorizeTeacherHomepageOwner(ctx context.Context, subject, school, teacher string) error {
	if subject == "" {
		return ErrUnauthenticated
	}
	if s.TeacherHomepageOwnership == nil {
		return ErrUnavailable
	}
	owns, err := s.TeacherHomepageOwnership.OwnsTeacher(ctx, subject, school, teacher)
	if err != nil {
		return ErrUnavailable
	}
	if !owns {
		return ErrForbidden
	}
	return nil
}

func (s *Service) ownTeacherHomepageTarget(ctx context.Context, p Principal, id string) (Object, error) {
	var teacher Object
	if p.Subject == "" {
		return teacher, ErrUnauthenticated
	}
	if !ValidID(id) {
		return teacher, ErrInput
	}
	err := s.Repo.Transaction(teacherMaintenanceContext(ctx), func(t Transaction) error {
		var err error
		teacher, err = requestTeacher(t, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return teacher, ErrForbidden
	}
	if err != nil {
		return teacher, err
	}
	// Verification resolves directory IDs using its own catalog read. Do not
	// call it while this operation holds a catalog transaction or school lock.
	return teacher, s.authorizeTeacherHomepageOwner(ctx, p.Subject, teacher.SchoolID, teacher.ID)
}

func recheckRequestTeacher(t Transaction, id string, target Object) error {
	teacher, err := requestTeacher(t, id)
	if errors.Is(err, ErrNotFound) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if teacher.SchoolID != target.SchoolID || teacher.ID != target.ID {
		return ErrForbidden
	}
	return nil
}

func normalizeTeacherHomepageRequest(in TeacherHomepageRequestInput) (TeacherHomepageRequestInput, *TeacherHomepageImage, error) {
	in.Message = strings.TrimSpace(in.Message)
	if in.Message == "" || !validText(in.Message, 2000) {
		return in, nil, ErrInput
	}
	switch in.Kind {
	case "hide":
		if in.Value != nil {
			return in, nil, ErrInput
		}
		if in.Field.Field == "page" && in.Field.Label == "" {
			return in, nil, nil
		}
	case "correction", "supplement":
		if in.Value == nil {
			return in, nil, ErrInput
		}
		if in.Field.Field == "photo" {
			if in.Field.Label != "" || in.Value.Photo == nil || in.Value.Text != nil ||
				in.Value.Fields != nil || in.Value.Sections != nil || in.Value.Supervisors != nil {
				return in, nil, ErrInput
			}
			upload := in.Value.Photo
			if upload.ID != "" || upload.Width != 0 || upload.Height != 0 {
				return in, nil, ErrInput
			}
			photo, err := ValidateTeacherHomepagePhotoUpload(upload.ContentType, upload.Data)
			if err != nil {
				return in, nil, err
			}
			in.Value = &TeacherHomepageFieldValue{Photo: &TeacherHomepageFieldPhoto{
				ID: photo.ID, ContentType: photo.MediaType, Width: photo.Width, Height: photo.Height,
			}}
			return in, &photo, nil
		}
	default:
		return in, nil, ErrInput
	}
	rule, err := normalizeTeacherHomepageRule(TeacherHomepageFieldRule{
		Field: in.Field, Hidden: in.Kind == "hide", Override: in.Value,
	})
	if err != nil {
		return in, nil, err
	}
	in.Field, in.Value = rule.Field, rule.Override
	return in, nil, nil
}

func (s *Service) SubmitTeacherHomepageRequest(ctx context.Context, p Principal, id, key string, in TeacherHomepageRequestInput) (TeacherHomepageRequest, error) {
	var out TeacherHomepageRequest
	target, err := s.ownTeacherHomepageTarget(ctx, p, id)
	if err != nil {
		return out, err
	}
	if !ValidKey(key) {
		return out, ErrInput
	}
	in, photo, err := normalizeTeacherHomepageRequest(in)
	if err != nil {
		return out, err
	}
	encoded, err := json.Marshal(in)
	if err != nil || len(encoded) > 1<<20 {
		return out, ErrInput
	}
	replayKey := "teacher-homepage-request-submit:" + digest([]string{p.Subject, target.SchoolID, target.ID, key})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	err = s.Repo.Transaction(teacherMaintenanceContext(ctx), func(t Transaction) error {
		// The same account may have verified bindings in different schools.
		if err := t.Lock("teacher-homepage-request-rate:" + digest([]string{p.Subject})); err != nil {
			return err
		}
		if err := t.Lock(target.SchoolID); err != nil {
			return err
		}
		if err := recheckRequestTeacher(t, id, target); err != nil {
			return err
		}
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		previous, err := t.Replay(replayKey)
		if err == nil {
			if previous.Digest != fingerprint {
				return ErrConflict
			}
			out, err = port.GetTeacherHomepageRequest(target.SchoolID, string(previous.Result))
			if errors.Is(err, ErrNotFound) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			if out.TeacherID != target.ID || out.Submitter != p.Subject {
				return ErrUnavailable
			}
			out.Status, out.Revision, out.ResolvedAt, out.ResolutionNote = "pending", 1, nil, ""
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := s.now().Truncate(time.Microsecond)
		count, err := port.TeacherHomepageRequestsSince(p.Subject, now.Add(-time.Minute), teacherHomepageRequestSubmissionLimit)
		if err != nil {
			return err
		}
		if count >= teacherHomepageRequestSubmissionLimit {
			return ErrRateLimited
		}
		out = TeacherHomepageRequest{
			TeacherHomepageRequestInput: in,
			ID:                          "threq-" + rand.Text(), SchoolID: target.SchoolID, TeacherID: target.ID, Submitter: p.Subject,
			Status: "pending", Revision: 1, CreatedAt: now,
		}
		if err = port.InsertTeacherHomepageRequest(out); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrForbidden
			}
			return err
		}
		if photo != nil {
			if err := port.SaveTeacherHomepageRequestPhoto(out.SchoolID, out.ID, *photo); err != nil {
				return err
			}
		}
		audit, err := refreshPort(t)
		if err != nil {
			return err
		}
		if err = audit.RecordTeacherRefreshAudit(Event{
			ID: out.ID + ":submitted", Subject: "teacher-homepage-request/" + out.ID,
			Type: "catalog.teacher-homepage-request.submitted.v1", SchoolID: out.SchoolID, Actor: p.Subject,
			Revision: out.Revision, Time: out.CreatedAt, Result: "success",
		}); err != nil {
			return err
		}
		return saveTeacherHomepageRequestReplay(t, port, replayKey, fingerprint, out.ID)
	})
	return out, err
}

func saveTeacherHomepageRequestReplay(t Transaction, port TeacherHomepageRequestTransaction, key, fingerprint, id string) error {
	if err := t.SaveReplay(key, Replay{Digest: fingerprint, Result: []byte(id)}); err != nil {
		return err
	}
	return port.LinkTeacherHomepageRequestReplay(id, key)
}

func normalizeTeacherHomepageRequestDecision(in TeacherHomepageRequestDecisionInput) (TeacherHomepageRequestDecisionInput, error) {
	in.Note = strings.TrimSpace(in.Note)
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 1 || in.Note == "" || !validText(in.Note, 2000) {
		return in, ErrInput
	}
	switch in.Action {
	case "approve":
		if in.ExpectedHomepageRevision == nil || *in.ExpectedHomepageRevision < 0 {
			return in, ErrInput
		}
	case "reject":
		if in.ExpectedHomepageRevision != nil {
			return in, ErrInput
		}
	default:
		return in, ErrInput
	}
	return in, nil
}

func (s *Service) ResolveTeacherHomepageRequest(ctx context.Context, p Principal, school, id, key string, in TeacherHomepageRequestDecisionInput) (TeacherHomepageRequest, error) {
	var out TeacherHomepageRequest
	if err := s.teacherRefreshAccess(ctx, p, school, "request-resolved", p.CanMaintain); err != nil {
		return out, err
	}
	if !ValidID(id) || !ValidKey(key) {
		return out, ErrInput
	}
	in, err := normalizeTeacherHomepageRequestDecision(in)
	if err != nil {
		return out, err
	}
	replayKey := "teacher-homepage-request-resolve:" + digest([]string{p.Subject, school, key})
	fingerprint := digest([]any{id, in})
	if in.Action == "approve" {
		if err := s.verifyTeacherHomepageRequestApproval(ctx, school, id, *in.ExpectedRevision); err != nil {
			return out, err
		}
	}
	err = s.Repo.Transaction(teacherMaintenanceContext(ctx), func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		previous, err := t.Replay(replayKey)
		if err == nil {
			if previous.Digest != fingerprint || string(previous.Result) != id {
				return ErrConflict
			}
			out, err = port.GetTeacherHomepageRequest(school, id)
			return err
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		out, err = port.GetTeacherHomepageRequest(school, id)
		if err != nil {
			return err
		}
		if out.Status != "pending" || out.Revision != *in.ExpectedRevision {
			return ErrConflict
		}
		now := s.now().Truncate(time.Microsecond)
		if now.Before(out.CreatedAt) {
			now = out.CreatedAt
		}
		out.Status = "rejected"
		if in.Action == "approve" {
			if err := s.approveTeacherHomepageRequest(t, port, p, out, *in.ExpectedHomepageRevision, now); err != nil {
				return err
			}
			out.Status = "approved"
		}
		out.Revision++
		out.ResolvedAt, out.ResolutionNote = &now, in.Note
		if err = port.ResolveTeacherHomepageRequest(out); err != nil {
			return err
		}
		if err = t.Event(Event{
			ID: fmt.Sprintf("%s:resolved:%d", id, out.Revision), Subject: "teacher-homepage-request/" + id,
			Type: TeacherHomepageRequestResolved, SchoolID: school, Actor: p.Subject,
			Revision: out.Revision, Time: now, Result: "success", Reason: out.Status,
		}); err != nil {
			return err
		}
		return saveTeacherHomepageRequestReplay(t, port, replayKey, fingerprint, out.ID)
	})
	return out, err
}

func (s *Service) verifyTeacherHomepageRequestApproval(ctx context.Context, school, id string, expected int64) error {
	var request TeacherHomepageRequest
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		request, err = port.GetTeacherHomepageRequest(school, id)
		return err
	})
	if err != nil {
		return err
	}
	// A finished request can only replay or conflict. A hide request can be
	// processed without creating a new ground for publication.
	if request.Kind == "hide" || request.Status != "pending" || request.Revision != expected {
		return nil
	}
	teacher, err := s.ownTeacherHomepageTarget(ctx, Principal{Subject: request.Submitter}, request.TeacherID)
	if err != nil {
		return err
	}
	if teacher.ID != request.TeacherID || teacher.SchoolID != school {
		return ErrConflict
	}
	return nil
}

func (s *Service) approveTeacherHomepageRequest(t Transaction, requests TeacherHomepageRequestTransaction, p Principal, request TeacherHomepageRequest, expected int64, now time.Time) error {
	port, ok := t.(TeacherHomepageControlsTransaction)
	if !ok {
		return ErrUnavailable
	}
	next, err := port.TeacherHomepageControls(request.SchoolID, request.TeacherID)
	if err != nil {
		return err
	}
	if next.Revision != expected {
		return ErrConflict
	}
	var approved *TeacherHomepageImage
	if request.Value != nil && request.Value.Photo != nil {
		photo, err := requests.ReadTeacherHomepageRequestPhoto(request.SchoolID, request.ID)
		if err != nil {
			return err
		}
		if photo.ID != request.Value.Photo.ID || photo.MediaType != request.Value.Photo.ContentType ||
			photo.Width != request.Value.Photo.Width || photo.Height != request.Value.Photo.Height {
			return ErrUnavailable
		}
		approved = &photo
		request.Value = &TeacherHomepageFieldValue{Photo: &TeacherHomepageFieldPhoto{ID: photo.ID}}
	}
	if request.Field.Field == "page" {
		if next.PageVisibility != TeacherHomepageStopped {
			next.PageVisibility = TeacherHomepageHidden
		}
	} else {
		next.Rules = applyTeacherHomepageRequestField(next.Rules, request.TeacherHomepageRequestInput)
	}
	if teacherHomepageRequestAddsContent(request.Value) {
		publication, ok := t.(TeacherHomepagePublicationTransaction)
		if !ok {
			return ErrUnavailable
		}
		if err := publication.GrantVerifiedTeacherHomepage(request.SchoolID, request.TeacherID, now); err != nil {
			return err
		}
	}
	_, err = s.applyTeacherHomepageControlsWithPhoto(t, p, request.SchoolID, request.TeacherID, expected, next, now, approved)
	return err
}

func teacherHomepageRequestAddsContent(value *TeacherHomepageFieldValue) bool {
	if value == nil {
		return false
	}
	return value.Text != nil || value.Photo != nil ||
		value.Supervisors != nil && len(*value.Supervisors) > 0 ||
		value.Fields != nil && len(*value.Fields) > 0 ||
		value.Sections != nil && len(*value.Sections) > 0
}

func applyTeacherHomepageRequestField(rules []TeacherHomepageFieldRule, request TeacherHomepageRequestInput) []TeacherHomepageFieldRule {
	key := teacherHomepageSelectorKey(request.Field)
	for index, current := range rules {
		if teacherHomepageSelectorKey(current.Field) != key {
			continue
		}
		if request.Kind == "hide" {
			current.Hidden = true
		} else {
			current.Override = request.Value
		}
		rules[index] = current
		return rules
	}
	return append(rules, TeacherHomepageFieldRule{
		Field: request.Field, Hidden: request.Kind == "hide", Override: request.Value,
	})
}
