package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
)

type TeacherHomepageComplaintReference struct {
	TeacherID string
	SchoolID  string
	Revision  int64
	Visible   bool
}

type teacherHomepageComplaintSource interface {
	TeacherHomepageSourceRevision(school, teacherID string) (string, int64, error)
}

// InspectTeacherHomepageComplaint is a host-only port. The host supplies the
// logged-in identity and its configured school. Only directory identity and
// revision metadata leave Catalog.
func (s *Service) InspectTeacherHomepageComplaint(ctx context.Context, p Principal, school, teacherID string) (TeacherHomepageComplaintReference, error) {
	var out TeacherHomepageComplaintReference
	if err := teacherHomepageComplaintScope(p, school, teacherID); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		refresh, err := refreshPort(t)
		if err != nil {
			return err
		}
		if err := refresh.BeginTeacherRefreshSnapshot(); err != nil {
			return err
		}
		out, _, err = readTeacherHomepageComplaint(t, school, teacherID)
		return err
	})
	return out, err
}

// ApplyTeacherHomepageComplaintHide applies a moderation delivery once. Its
// decision identity and revision come from the trusted moderation worker.
func (s *Service) ApplyTeacherHomepageComplaintHide(ctx context.Context, p Principal, school, teacherID, decisionID string, revision int64) error {
	if err := teacherHomepageComplaintScope(p, school, teacherID); err != nil {
		return err
	}
	if !p.CanMaintain {
		return ErrForbidden
	}
	if !ValidKey(decisionID) || revision < 1 || revision > 1<<53-1 {
		return ErrInput
	}
	key := "teacher-homepage-complaint:" + digest([]string{school, decisionID})
	fingerprint := digest([]any{teacherID, revision})
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		replay, err := t.Replay(key)
		if err == nil {
			if replay.Digest != fingerprint {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		target, controls, err := readTeacherHomepageComplaint(t, school, teacherID)
		if err != nil {
			return err
		}
		if target.Revision != revision {
			return ErrConflict
		}
		now := s.now()
		if controls.PageVisibility == TeacherHomepageVisible {
			controls.PageVisibility = TeacherHomepageHidden
			if _, err := s.applyTeacherHomepageControls(t, p, school, teacherID, controls.Revision, controls, now); err != nil {
				return err
			}
		}
		audit, err := refreshPort(t)
		if err != nil {
			return err
		}
		if err := audit.RecordTeacherRefreshAudit(Event{
			ID: key, Subject: "teacher/" + teacherID, SchoolID: school, Actor: p.Subject,
			Type: "catalog.teacher-sites.homepage-complaint-hidden.v1", Revision: revision,
			Result: "success", Time: now,
		}); err != nil {
			return err
		}
		return t.SaveReplay(key, Replay{Digest: fingerprint, Result: json.RawMessage(`{}`)})
	})
}

func teacherHomepageComplaintScope(p Principal, school, teacherID string) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if !ValidID(school) || !ValidID(teacherID) {
		return ErrInput
	}
	if !allowed(p, school) {
		return ErrForbidden
	}
	return nil
}

func readTeacherHomepageComplaint(t Transaction, school, teacherID string) (TeacherHomepageComplaintReference, TeacherHomepageControls, error) {
	var reference TeacherHomepageComplaintReference
	var controls TeacherHomepageControls
	controlsPort, okControls := t.(TeacherHomepageControlsTransaction)
	homepagesPort, okHomepages := t.(TeacherHomepageTransaction)
	sourcePort, okSource := t.(teacherHomepageComplaintSource)
	if !okControls || !okHomepages || !okSource {
		return reference, controls, ErrUnavailable
	}
	record, err := t.Object(teacherID)
	if err != nil {
		return reference, controls, err
	}
	teacher := record.Object
	if teacher.ID != teacherID || teacher.Kind != "teacher" || teacher.SchoolID != school || teacher.Inactive {
		return reference, controls, ErrNotFound
	}
	// This port checks the active canonical teacher, including when the public
	// directory does not currently display that teacher.
	controls, err = controlsPort.TeacherHomepageControls(school, teacherID)
	if err != nil {
		return reference, controls, err
	}
	list, err := loadTeacherHomepageAllowlist(t, school)
	if err != nil {
		return reference, controls, err
	}
	siteID, sourceRevision, err := sourcePort.TeacherHomepageSourceRevision(school, teacherID)
	if err != nil {
		return reference, controls, err
	}
	if siteID == "" && !controls.Independent {
		return reference, controls, ErrNotFound
	}
	presentation := newHomepagePresentation(controls, list)
	if presentation.permitsPage() {
		source, err := homepagesPort.TeacherHomepage(school, teacherID)
		if err != nil {
			return reference, controls, err
		}
		presentation.directory, err = teacherDisplayDirectory(t, teacher)
		if err != nil {
			return reference, controls, err
		}
		reference.Visible = presentation.profile(source, teacherID) != nil
	}

	// Decisions use equality on this opaque token. The underlying revisions
	// are independent counters and the site ID can change on a manual rebind.
	parts, err := json.Marshal([]any{school, teacherID, teacher.Revision, controls.Revision, list.Revision, siteID, sourceRevision})
	if err != nil {
		return reference, controls, ErrUnavailable
	}
	sum := sha256.Sum256(parts)
	reference.TeacherID = teacherID
	reference.SchoolID = school
	reference.Revision = 1 + int64(binary.BigEndian.Uint64(sum[:8])%((1<<53)-1))
	return reference, controls, nil
}
