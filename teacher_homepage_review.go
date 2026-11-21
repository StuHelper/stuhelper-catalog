package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

type TeacherReviewQuery struct {
	SchoolID, Status, Query, After string
	Limit                          int
}

type TeacherSiteReviewCandidate struct {
	TeacherID    string `json:"teacher_id"`
	Name         string `json:"name"`
	DepartmentID string `json:"department_id,omitempty"`
	Department   string `json:"department,omitempty"`
	Title        string `json:"title,omitempty"`
}

type TeacherReviewCandidate struct {
	SiteID  string `json:"site_id"`
	Name    string `json:"name"`
	Unit    string `json:"unit"`
	Present bool   `json:"present"`
}

type TeacherSiteReview struct {
	SiteID           string                       `json:"site_id"`
	Name             string                       `json:"name"`
	Unit             string                       `json:"unit"`
	Reason           string                       `json:"reason,omitempty"`
	MatchedTeacherID string                       `json:"matched_teacher_id,omitempty"`
	BiographyExcerpt string                       `json:"biography_excerpt,omitempty"`
	Discipline       string                       `json:"discipline,omitempty"`
	Present          bool                         `json:"present"`
	Decision         *TeacherSiteDecisionValue    `json:"decision,omitempty"`
	DecisionRevision int64                        `json:"decision_revision"`
	CandidateCount   int64                        `json:"candidate_count"`
	Candidates       []TeacherSiteReviewCandidate `json:"candidates"`
}

type TeacherReview struct {
	TeacherID      string                   `json:"teacher_id"`
	Name           string                   `json:"name"`
	DepartmentID   string                   `json:"department_id,omitempty"`
	Department     string                   `json:"department,omitempty"`
	Title          string                   `json:"title,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
	MatchedSiteID  string                   `json:"matched_site_id,omitempty"`
	CandidateCount int64                    `json:"candidate_count"`
	Candidates     []TeacherReviewCandidate `json:"candidates"`
}

type TeacherSiteReviewPage struct {
	Items    []TeacherSiteReview `json:"items"`
	Next     string              `json:"next"`
	Total    int64               `json:"total"`
	Revision int64               `json:"-"`
}

type TeacherReviewPage struct {
	Items    []TeacherReview `json:"items"`
	Next     string          `json:"next"`
	Total    int64           `json:"total"`
	Revision int64           `json:"-"`
}

type TeacherSiteDecisionInput struct {
	Action           string `json:"action"`
	TeacherID        string `json:"teacher_id,omitempty"`
	ExpectedRevision *int64 `json:"expected_revision"`
}

type TeacherSiteDecisionValue struct {
	Action    string `json:"action"`
	TeacherID string `json:"teacher_id,omitempty"`
}

type TeacherSiteDecision struct {
	SiteID           string                    `json:"site_id"`
	DecisionRevision int64                     `json:"decision_revision"`
	Decision         *TeacherSiteDecisionValue `json:"decision,omitempty"`
}

type TeacherHomepageReviewTransaction interface {
	ReadTeacherSiteReviews(TeacherReviewQuery) (TeacherSiteReviewPage, error)
	ReadTeacherReviews(TeacherReviewQuery) (TeacherReviewPage, error)
	SetTeacherSiteDecision(school, siteID string, in TeacherSiteDecisionInput, now time.Time) (TeacherSiteDecision, error)
}

type teacherReviewCursor struct {
	School, Kind, Status, Query, ID string
	Revision                        int64
}

func (s *Service) readTeacherReview(ctx context.Context, p Principal, in TeacherReviewQuery, kind string, statuses []string, load func(TeacherHomepageReviewTransaction, TeacherReviewQuery) (string, int64, error)) (string, error) {
	action := "review-" + kind + "-viewed"
	if err := s.teacherRefreshAccess(ctx, p, in.SchoolID, action, p.CanReadTeacherSites); err != nil {
		return "", err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Status == "" {
		in.Status = statuses[0]
	}
	if !slices.Contains(statuses, in.Status) || !validText(in.Query, 200) || in.Limit < 1 || in.Limit > 100 || len(in.After) > 4096 {
		return "", ErrInput
	}
	cursor := teacherReviewCursor{}
	if in.After != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(in.After)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Kind != kind || cursor.School != in.SchoolID ||
			cursor.Status != in.Status || cursor.Query != in.Query || !ValidID(cursor.ID) ||
			kind == "sites" && !validTeacherSiteID(cursor.ID) || cursor.Revision < 0 {
			return "", ErrInput
		}
	}
	in.After = cursor.ID
	in.Limit++
	next := ""
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		refresh, err := refreshPort(t)
		if err != nil {
			return err
		}
		port, ok := t.(TeacherHomepageReviewTransaction)
		if !ok {
			return ErrUnavailable
		}
		if err := refresh.BeginTeacherRefreshSnapshot(); err != nil {
			return err
		}
		lastID, revision, err := load(port, in)
		if err != nil {
			return err
		}
		if cursor.ID != "" && revision != cursor.Revision {
			return ErrConflict
		}
		if lastID != "" {
			value := teacherReviewCursor{School: in.SchoolID, Kind: kind, Status: in.Status, Query: in.Query, ID: lastID, Revision: revision}
			raw, err := json.Marshal(value)
			if err != nil {
				return ErrUnavailable
			}
			next = base64.RawURLEncoding.EncodeToString(raw)
		}
		return s.teacherRefreshAudit(t, p, in.SchoolID, action, "school/"+in.SchoolID, "success")
	})
	return next, err
}

func (s *Service) ReadTeacherSiteReviews(ctx context.Context, p Principal, in TeacherReviewQuery) (TeacherSiteReviewPage, error) {
	var out TeacherSiteReviewPage
	next, err := s.readTeacherReview(ctx, p, in, "sites", []string{"pending", "decided", "all"}, func(port TeacherHomepageReviewTransaction, query TeacherReviewQuery) (string, int64, error) {
		var err error
		out, err = port.ReadTeacherSiteReviews(query)
		if err != nil {
			return "", 0, err
		}
		items, hasMore, err := boundedTeacherReviewItems(out.Items, in.Limit)
		if err != nil {
			return "", 0, err
		}
		out.Items = items
		if hasMore {
			return items[len(items)-1].SiteID, out.Revision, nil
		}
		return "", out.Revision, nil
	})
	out.Next = next
	return out, err
}

func (s *Service) ReadTeacherReviews(ctx context.Context, p Principal, in TeacherReviewQuery) (TeacherReviewPage, error) {
	var out TeacherReviewPage
	next, err := s.readTeacherReview(ctx, p, in, "teachers", []string{"unmatched", "all"}, func(port TeacherHomepageReviewTransaction, query TeacherReviewQuery) (string, int64, error) {
		var err error
		out, err = port.ReadTeacherReviews(query)
		if err != nil {
			return "", 0, err
		}
		items, hasMore, err := boundedTeacherReviewItems(out.Items, in.Limit)
		if err != nil {
			return "", 0, err
		}
		out.Items = items
		if hasMore {
			return items[len(items)-1].TeacherID, out.Revision, nil
		}
		return "", out.Revision, nil
	})
	out.Next = next
	return out, err
}

func boundedTeacherReviewItems[T any](items []T, limit int) ([]T, bool, error) {
	// The Admin JSON reader allows four MiB. Keep room for page metadata and
	// preserve the complete candidate list for every row returned.
	budget := 3 << 20
	if items == nil {
		return []T{}, false, nil
	}
	for i := range min(len(items), limit) {
		raw, err := json.Marshal(items[i])
		if err != nil || i == 0 && len(raw)+1 > budget {
			return nil, false, ErrUnavailable
		}
		budget -= len(raw) + 1
		if budget < 0 {
			return items[:i], true, nil
		}
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

func (s *Service) SetTeacherSiteDecision(ctx context.Context, p Principal, school, siteID, key string, in TeacherSiteDecisionInput) (TeacherSiteDecision, error) {
	var out TeacherSiteDecision
	action := "decision"
	switch in.Action {
	case "bind", "reject", "revoke":
		action += "-" + in.Action
	}
	if err := s.teacherRefreshAccess(ctx, p, school, action, p.CanMaintain); err != nil {
		return out, err
	}
	if action == "decision" || !validTeacherSiteID(siteID) || !ValidKey(key) || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 ||
		in.Action == "bind" && !ValidID(in.TeacherID) || in.Action != "bind" && in.TeacherID != "" {
		return out, ErrInput
	}
	replayKey := "teacher-site-decision:" + digest([]string{p.Subject, school, key})
	fingerprint := digest([]any{siteID, in})
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(school); err != nil {
			return err
		}
		cached, err := t.Replay(replayKey)
		if err == nil {
			if cached.Digest != fingerprint {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &out)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		port, ok := t.(TeacherHomepageReviewTransaction)
		if !ok {
			return ErrUnavailable
		}
		out, err = port.SetTeacherSiteDecision(school, siteID, in, s.now())
		if err != nil {
			return err
		}
		target := "teacher-site/" + siteID
		if in.Action == "bind" {
			target += "/teacher/" + in.TeacherID
		}
		if err := s.teacherRefreshAudit(t, p, school, action, target, "success"); err != nil {
			return err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return t.SaveReplay(replayKey, Replay{Digest: fingerprint, Result: data})
	})
	return out, err
}
