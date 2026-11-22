package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type teacherHomepageRequestCursor struct {
	Scope string    `json:"s"`
	At    time.Time `json:"t"`
	ID    string    `json:"i"`
}

func prepareTeacherHomepageRequestQuery(q TeacherHomepageRequestQuery, viewer, defaultStatus string) (TeacherHomepageRequestQuery, string, error) {
	if q.TeacherID != "" && !ValidID(q.TeacherID) || q.Limit < 1 || q.Limit > 100 || len(q.After) > 512 || !q.AfterCreatedAt.IsZero() {
		return q, "", ErrInput
	}
	if q.Status == "" {
		q.Status = defaultStatus
	}
	switch q.Status {
	case "all":
		q.Status = ""
	case "pending", "approved", "rejected":
	default:
		return q, "", ErrInput
	}
	scope := digest([]string{viewer, q.SchoolID, q.TeacherID, q.Submitter, q.Status})
	if q.After != "" {
		var cursor teacherHomepageRequestCursor
		raw, err := base64.RawURLEncoding.Strict().DecodeString(q.After)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Scope != scope || cursor.At.IsZero() || !ValidID(cursor.ID) {
			return q, "", ErrInput
		}
		q.AfterCreatedAt, q.After = cursor.At, cursor.ID
	}
	return q, scope, nil
}

func loadTeacherHomepageRequestPage(t Transaction, q TeacherHomepageRequestQuery, scope string) (TeacherHomepageRequestPage, error) {
	var out TeacherHomepageRequestPage
	port, err := homepageRequestPort(t)
	if err != nil {
		return out, err
	}
	items, total, hasMore, err := port.ListTeacherHomepageRequests(q)
	if err != nil {
		return out, err
	}
	items, more, err := boundedTeacherReviewItems(items, q.Limit)
	if err != nil {
		return out, err
	}
	out.Items, out.Total = items, total
	if more || hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cursor, err := json.Marshal(teacherHomepageRequestCursor{Scope: scope, At: last.CreatedAt, ID: last.ID})
		if err != nil {
			return out, ErrUnavailable
		}
		out.Next = base64.RawURLEncoding.EncodeToString(cursor)
	}
	return out, nil
}

func (s *Service) ListMyTeacherHomepageRequests(ctx context.Context, p Principal, id string, q TeacherHomepageRequestQuery) (TeacherHomepageRequestPage, error) {
	var out TeacherHomepageRequestPage
	teacher, err := s.ownTeacherHomepageTarget(ctx, p, id)
	if err != nil {
		return out, err
	}
	q.SchoolID, q.TeacherID, q.Submitter = teacher.SchoolID, teacher.ID, p.Subject
	q, scope, err := prepareTeacherHomepageRequestQuery(q, p.Subject, "all")
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(teacherMaintenanceContext(ctx), func(t Transaction) error {
		if err := recheckRequestTeacher(t, id, teacher); err != nil {
			return err
		}
		var err error
		out, err = loadTeacherHomepageRequestPage(t, q, scope)
		return err
	})
	return out, err
}

func (s *Service) GetMyTeacherHomepageRequest(ctx context.Context, p Principal, id string) (TeacherHomepageRequest, error) {
	var out TeacherHomepageRequest
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	if !ValidID(id) {
		return out, ErrInput
	}
	var request TeacherHomepageRequest
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		request, err = port.ReadTeacherHomepageRequestForOwner(p.Subject, id)
		return err
	})
	if err != nil {
		return out, err
	}
	teacher, err := s.ownTeacherHomepageTarget(ctx, p, request.TeacherID)
	if err != nil {
		return out, err
	}
	// An old receipt remains bound to the directory object it named at submission.
	if teacher.SchoolID != request.SchoolID || teacher.ID != request.TeacherID {
		return out, ErrNotFound
	}
	return request, nil
}

func (s *Service) ListTeacherHomepageRequests(ctx context.Context, p Principal, school string, q TeacherHomepageRequestQuery) (TeacherHomepageRequestPage, error) {
	var out TeacherHomepageRequestPage
	if err := s.teacherRefreshAccess(ctx, p, school, "requests-viewed", p.CanMaintain); err != nil {
		return out, err
	}
	q.SchoolID, q.Submitter = school, ""
	q, scope, err := prepareTeacherHomepageRequestQuery(q, p.Subject, "pending")
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		out, err = loadTeacherHomepageRequestPage(t, q, scope)
		if err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, school, "requests-viewed", "school/"+school, "success")
	})
	return out, err
}

func (s *Service) GetTeacherHomepageRequest(ctx context.Context, p Principal, school, id string) (TeacherHomepageRequest, error) {
	var out TeacherHomepageRequest
	if err := s.teacherRefreshAccess(ctx, p, school, "request-viewed", p.CanMaintain); err != nil {
		return out, err
	}
	if !ValidID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		out, err = port.GetTeacherHomepageRequest(school, id)
		if err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, school, "request-viewed", "teacher-homepage-request/"+id, "success")
	})
	return out, err
}

// TeacherHomepageRequestNotification identifies the recipient of a trusted
// resolution event without carrying the request contents into the notification.
type TeacherHomepageRequestNotification struct {
	Owner, SchoolID, TeacherID, Status string
}

func (s *Service) TeacherHomepageRequestNotification(ctx context.Context, requestID string, revision int64) (TeacherHomepageRequestNotification, bool, error) {
	var out TeacherHomepageRequestNotification
	if !ValidID(requestID) || revision < 1 {
		return out, false, ErrInput
	}
	var row TeacherHomepageRequestRef
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		row, err = port.FindTeacherHomepageRequest(requestID)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return out, false, nil
	}
	if err != nil || row.Revision != revision || !teacherHomepageRequestResolved(row.Status) {
		return out, false, err
	}
	if row.Submitter == "" || !ValidID(row.SchoolID) || !ValidID(row.TeacherID) {
		return out, false, ErrUnavailable
	}
	return TeacherHomepageRequestNotification{Owner: row.Submitter, SchoolID: row.SchoolID, TeacherID: row.TeacherID, Status: row.Status}, true, nil
}

// AccessibleTeacherHomepageRequests is used for private notification links.
// It checks both the stored owner and the owner's current verified binding.
func (s *Service) AccessibleTeacherHomepageRequests(ctx context.Context, owner string, ids []string) ([]string, error) {
	if owner == "" || len(ids) > 100 {
		return nil, ErrInput
	}
	for _, id := range ids {
		if !ValidID(id) {
			return nil, ErrInput
		}
	}
	out := make([]string, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []TeacherHomepageRequestRef
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := homepageRequestPort(t)
		if err != nil {
			return err
		}
		rows, err = port.ReadTeacherHomepageRequestsForOwner(owner, ids)
		return err
	})
	if err != nil {
		return nil, err
	}
	allowed := make(map[[2]string]bool)
	for _, row := range rows {
		if !teacherHomepageRequestResolved(row.Status) {
			continue
		}
		key := [2]string{row.SchoolID, row.TeacherID}
		owns, checked := allowed[key]
		if !checked {
			teacher, err := s.ownTeacherHomepageTarget(ctx, Principal{Subject: owner}, row.TeacherID)
			if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			owns = err == nil && teacher.ID == row.TeacherID && teacher.SchoolID == row.SchoolID
			allowed[key] = owns
		}
		if owns {
			out = append(out, row.ID)
		}
	}
	return out, nil
}

func teacherHomepageRequestResolved(status string) bool {
	return status == "approved" || status == "rejected"
}
