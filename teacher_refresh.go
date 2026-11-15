package catalog

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

const TeacherRefreshTimeout = 45 * time.Minute

var TeacherUnmatchedReasons = []string{"catalog_name_absent", "catalog_unit_missing", "site_unit_missing", "unit_mismatch", "ambiguous_catalog", "ambiguous_site"}

type TeacherRefreshRun struct {
	ID            string                  `json:"id"`
	SchoolID      string                  `json:"school_id"`
	Mode          string                  `json:"mode"`
	Trigger       string                  `json:"trigger"`
	Status        string                  `json:"status"`
	RequestedAt   time.Time               `json:"requested_at"`
	StartedAt     *time.Time              `json:"started_at"`
	FinishedAt    *time.Time              `json:"finished_at"`
	DeadlineAt    time.Time               `json:"deadline_at"`
	DurationMS    int64                   `json:"duration_ms"`
	Requests      *int64                  `json:"requests"`
	SiteCount     *int64                  `json:"site_count"`
	Counts        *TeacherDirectoryResult `json:"counts"`
	FailureReason string                  `json:"failure_reason"`
}

type TeacherRefreshStatus struct {
	Latest *TeacherRefreshRun     `json:"latest"`
	Counts TeacherDirectoryResult `json:"counts"`
}

type UnmatchedTeacherSite struct {
	SiteID         string `json:"site_id"`
	Name           string `json:"name"`
	Unit           string `json:"unit"`
	Reason         string `json:"reason"`
	CandidateCount int64  `json:"candidate_count"`
}

type UnmatchedTeacherSitePage struct {
	Items        []UnmatchedTeacherSite `json:"items"`
	Total        int64                  `json:"total"`
	ReasonCounts map[string]int64       `json:"reason_counts"`
	Next         string                 `json:"next"`
	Revision     int64                  `json:"revision"`
	ObservedAt   *time.Time             `json:"observed_at"`
}

type TeacherUnmatchedSnapshot struct {
	SchoolID   string                 `json:"school_id"`
	Revision   int64                  `json:"revision"`
	ObservedAt time.Time              `json:"observed_at"`
	Items      []UnmatchedTeacherSite `json:"items"`
}

// This repository port never executes network requests or accepts candidate
// identifiers in the review projection. The owning module persists all state.
type TeacherRefreshTransaction interface {
	BeginTeacherRefreshSnapshot() error
	RecordTeacherRefreshAudit(Event) error
	TeacherRefreshLatest(string) (*TeacherRefreshRun, error)
	TeacherRefresh(string) (TeacherRefreshRun, error)
	ActiveTeacherRefresh() (*TeacherRefreshRun, error)
	SaveTeacherRefresh(TeacherRefreshRun) error
	TeacherMarkCounts(string) (TeacherDirectoryResult, error)
	ReplaceTeacherUnmatched(TeacherUnmatchedSnapshot) error
	TeacherUnmatched(school, reason, after string, limit int) (UnmatchedTeacherSitePage, error)
}

func refreshPort(t Transaction) (TeacherRefreshTransaction, error) {
	port, ok := t.(TeacherRefreshTransaction)
	if !ok {
		return nil, ErrUnavailable
	}
	return port, nil
}

func (s *Service) teacherRefreshAudit(t Transaction, p Principal, school, action, target, result string) error {
	port, err := refreshPort(t)
	if err != nil {
		return err
	}
	return port.RecordTeacherRefreshAudit(Event{ID: "teacher-refresh:" + rand.Text(), Subject: target, Type: "catalog.teacher-sites." + action + ".v1", SchoolID: school, Actor: p.Subject, Result: result, Time: s.now()})
}

func (s *Service) teacherRefreshAccess(ctx context.Context, p Principal, school, action string, permitted bool) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if !ValidID(school) {
		return ErrInput
	}
	if permitted && allowed(p, school) {
		return nil
	}
	// A denied read or trigger is an audit event as well; it contains no website
	// names, candidate identities, request body, credentials or contact fields.
	if err := s.Repo.Transaction(ctx, func(t Transaction) error {
		return s.teacherRefreshAudit(t, p, school, action, "school/"+school, "denied")
	}); err != nil {
		return err
	}
	return ErrForbidden
}

func (s *Service) TeacherRefreshStatus(ctx context.Context, p Principal, school string) (TeacherRefreshStatus, error) {
	var out TeacherRefreshStatus
	if err := s.teacherRefreshAccess(ctx, p, school, "status-viewed", p.CanReadTeacherSites); err != nil {
		return out, err
	}
	// Ordinary polling reads the last committed snapshot while a worker builds
	// its next projection. Only an actually expired job needs the writer lock.
	expired := false
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, e := refreshPort(t)
		if e != nil {
			return e
		}
		active, e := port.ActiveTeacherRefresh()
		if e != nil {
			return e
		}
		expired = active != nil && !s.now().Before(active.DeadlineAt)
		return nil
	})
	if err != nil {
		return out, err
	}
	if expired {
		err = s.Repo.Transaction(ctx, func(t Transaction) error {
			if e := t.Lock("teacher-refresh"); e != nil {
				return e
			}
			port, e := refreshPort(t)
			if e != nil {
				return e
			}
			_, e = s.expireTeacherRefresh(t, port)
			return e
		})
		if err != nil {
			return out, err
		}
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := refreshPort(t)
		if err != nil {
			return err
		}
		if err = port.BeginTeacherRefreshSnapshot(); err != nil {
			return err
		}
		out.Latest, err = port.TeacherRefreshLatest(school)
		if err != nil {
			return err
		}
		out.Counts, err = port.TeacherMarkCounts(school)
		if err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, school, "status-viewed", "school/"+school, "success")
	})
	return out, err
}

func (s *Service) expireTeacherRefresh(t Transaction, port TeacherRefreshTransaction) (*TeacherRefreshRun, error) {
	active, err := port.ActiveTeacherRefresh()
	if err != nil || active == nil {
		return active, err
	}
	now := s.now()
	if now.Before(active.DeadlineAt) {
		return active, nil
	}
	active.Status = "timed_out"
	active.FailureReason = "timeout"
	active.FinishedAt = &now
	if active.StartedAt != nil {
		active.DurationMS = max(0, now.Sub(*active.StartedAt).Milliseconds())
	}
	if err = port.SaveTeacherRefresh(*active); err != nil {
		return nil, err
	}
	err = s.teacherRefreshAudit(t, Principal{Subject: "urn:stuhelper:maintenance:teacher-refresh"}, active.SchoolID, "finished", active.ID, "failed")
	return nil, err
}

// QueueTeacherRefresh is durable. A single global transaction lock plus the
// database's partial unique index excludes manual/scheduled/multi-process races.
func (s *Service) QueueTeacherRefresh(ctx context.Context, p Principal, school, mode, trigger, key string) (TeacherRefreshRun, error) {
	var out TeacherRefreshRun
	if err := s.teacherRefreshAccess(ctx, p, school, "triggered", p.CanRefreshTeacherSites); err != nil {
		return out, err
	}
	if !ValidKey(key) || !slices.Contains([]string{"incremental", "full"}, mode) || !slices.Contains([]string{"manual", "scheduled"}, trigger) || trigger == "scheduled" && mode != "incremental" {
		return out, ErrInput
	}
	id := "teacher-refresh-" + digest([]string{p.Subject, school, key})
	var rejected error
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock("teacher-refresh"); err != nil {
			return err
		}
		port, err := refreshPort(t)
		if err != nil {
			return err
		}
		active, err := s.expireTeacherRefresh(t, port)
		if err != nil {
			return err
		}
		prior, err := port.TeacherRefresh(id)
		if err == nil {
			if prior.Mode != mode || prior.Trigger != trigger {
				rejected = ErrConflict
			} else {
				out = prior
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		} else if active != nil {
			rejected = ErrConflict
		} else {
			schoolObject, e := t.Object(school)
			if e != nil {
				return e
			}
			if schoolObject.Object.Kind != "school" {
				return ErrInput
			}
			now := s.now()
			out = TeacherRefreshRun{ID: id, SchoolID: school, Mode: mode, Trigger: trigger, Status: "queued", RequestedAt: now, DeadlineAt: now.Add(TeacherRefreshTimeout)}
			if err = port.SaveTeacherRefresh(out); err != nil {
				return err
			}
		}
		result := "success"
		if rejected != nil {
			result = "denied"
		}
		return s.teacherRefreshAudit(t, p, school, "triggered", id, result)
	})
	if err != nil {
		return TeacherRefreshRun{}, err
	}
	return out, rejected
}

// ClaimTeacherRefresh is a maintenance-only port used by the isolated worker.
// It never retries a running job after a crash: the fixed deadline fences it.
func (s *Service) ClaimTeacherRefresh(ctx context.Context, p Principal, school string) (*TeacherRefreshRun, error) {
	if p.Subject == "" || !p.CanImport || !allowed(p, school) {
		return nil, ErrForbidden
	}
	var out *TeacherRefreshRun
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock("teacher-refresh"); err != nil {
			return err
		}
		port, err := refreshPort(t)
		if err != nil {
			return err
		}
		active, err := s.expireTeacherRefresh(t, port)
		if err != nil {
			return err
		}
		if active == nil || active.Status != "queued" || active.SchoolID != school {
			return nil
		}
		now := s.now()
		active.Status = "running"
		active.StartedAt = &now
		if err = port.SaveTeacherRefresh(*active); err != nil {
			return err
		}
		out = active
		return s.teacherRefreshAudit(t, p, school, "started", active.ID, "success")
	})
	return out, err
}

func validateUnmatched(in TeacherUnmatchedSnapshot, now time.Time) error {
	if !ValidID(in.SchoolID) || in.Revision < 1 || in.ObservedAt.IsZero() || in.ObservedAt.After(now.Add(5*time.Minute)) || len(in.Items) > 20000 {
		return ErrInput
	}
	seen := map[string]bool{}
	for _, site := range in.Items {
		if !ValidID(site.SiteID) || seen[site.SiteID] || strings.TrimSpace(site.Name) == "" || !validText(site.Name, 256) || !validText(site.Unit, 512) || !slices.Contains(TeacherUnmatchedReasons, site.Reason) || site.CandidateCount < 0 || site.CandidateCount > 100000 {
			return ErrInput
		}
		seen[site.SiteID] = true
	}
	return nil
}

func normalizedUnmatched(in TeacherUnmatchedSnapshot) TeacherUnmatchedSnapshot {
	in.Items = append([]UnmatchedTeacherSite{}, in.Items...)
	for i := range in.Items {
		// Website slugs sometimes resemble staff identifiers. The management API
		// needs only a stable row key, so persist a one-way school-scoped digest.
		in.Items[i].SiteID = "site-" + digest([]string{in.SchoolID, in.Items[i].SiteID})
	}
	slices.SortFunc(in.Items, func(a, b UnmatchedTeacherSite) int { return strings.Compare(a.SiteID, b.SiteID) })
	return in
}

// ImportTeacherUnmatched migrates an existing private report through the owning
// module, without creating a fake crawl run or changing existing teacher marks.
func (s *Service) ImportTeacherUnmatched(ctx context.Context, p Principal, in TeacherUnmatchedSnapshot) error {
	if p.Subject == "" || !p.CanImport || !allowed(p, in.SchoolID) {
		return ErrForbidden
	}
	if err := validateUnmatched(in, s.now()); err != nil {
		return err
	}
	in = normalizedUnmatched(in)
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(in.SchoolID); err != nil {
			return err
		}
		port, err := refreshPort(t)
		if err != nil {
			return err
		}
		if err = port.ReplaceTeacherUnmatched(in); err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, in.SchoolID, "unmatched-imported", "school/"+in.SchoolID, "success")
	})
}

type TeacherRefreshCompletion struct {
	Snapshot      *TeacherSiteSnapshot
	Unmatched     []UnmatchedTeacherSite
	Requests      *int64
	SiteCount     *int64
	FailureReason string
}

func (s *Service) CompleteTeacherRefresh(ctx context.Context, p Principal, id string, in TeacherRefreshCompletion) (TeacherRefreshRun, error) {
	var out TeacherRefreshRun
	if p.Subject == "" || !p.CanImport || !ValidID(id) {
		return out, ErrForbidden
	}
	if !slices.Contains([]string{"", "crawl_failed", "request_failed", "invalid_snapshot", "import_conflict", "dependency_unavailable", "worker_interrupted", "timeout"}, in.FailureReason) || in.Requests != nil && (*in.Requests < 0 || *in.Requests > 100000) || in.SiteCount != nil && (*in.SiteCount < 0 || *in.SiteCount > 20000) {
		return out, ErrInput
	}
	if in.FailureReason == "" {
		if in.Snapshot == nil || in.Requests == nil || in.SiteCount == nil || int64(len(in.Snapshot.Matches)+len(in.Unmatched)) != *in.SiteCount {
			return out, ErrInput
		}
		snapshot := *in.Snapshot
		in.Snapshot = &snapshot
		if err := s.validateTeacherSites(in.Snapshot); err != nil {
			return out, err
		}
		if err := validateUnmatched(TeacherUnmatchedSnapshot{SchoolID: in.Snapshot.SchoolID, Revision: in.Snapshot.Revision, ObservedAt: in.Snapshot.ObservedAt, Items: in.Unmatched}, s.now()); err != nil {
			return out, err
		}
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock("teacher-refresh"); err != nil {
			return err
		}
		port, err := refreshPort(t)
		if err != nil {
			return err
		}
		out, err = port.TeacherRefresh(id)
		if err != nil {
			return err
		}
		if !allowed(p, out.SchoolID) {
			return ErrForbidden
		}
		if out.Status != "running" || !s.now().Before(out.DeadlineAt) {
			return ErrConflict
		}
		if in.FailureReason == "" {
			if in.Snapshot.SchoolID != out.SchoolID {
				return ErrInput
			}
			if err = t.Lock(out.SchoolID); err != nil {
				return err
			}
			directory, ok := t.(TeacherDirectoryTransaction)
			if !ok {
				return ErrUnavailable
			}
			if _, _, err = directory.RefreshTeachingDirectory(out.SchoolID, s.now()); err != nil {
				return err
			}
			counts, e := directory.ReplaceTeacherSites(*in.Snapshot, p.Subject, s.now())
			if e != nil {
				return e
			}
			review := normalizedUnmatched(TeacherUnmatchedSnapshot{SchoolID: out.SchoolID, Revision: in.Snapshot.Revision, ObservedAt: in.Snapshot.ObservedAt, Items: in.Unmatched})
			if err = port.ReplaceTeacherUnmatched(review); err != nil {
				return err
			}
			out.Counts = &counts
		}
		now := s.now()
		if !now.Before(out.DeadlineAt) {
			return ErrConflict
		}
		out.FinishedAt = &now
		out.DurationMS = max(0, now.Sub(*out.StartedAt).Milliseconds())
		out.Requests = in.Requests
		out.SiteCount = in.SiteCount
		out.FailureReason = in.FailureReason
		out.Status = "succeeded"
		result := "success"
		if in.FailureReason != "" {
			out.Status = "failed"
			result = "failed"
		}
		if in.FailureReason == "timeout" {
			out.Status = "timed_out"
		}
		if err = port.SaveTeacherRefresh(out); err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, out.SchoolID, "finished", out.ID, result)
	})
	return out, err
}

type unmatchedCursor struct {
	School, Reason, ID string
	Revision           int64
}

func (s *Service) ReadUnmatchedTeacherSites(ctx context.Context, p Principal, school, reason, after string, limit int) (UnmatchedTeacherSitePage, error) {
	out := UnmatchedTeacherSitePage{Items: []UnmatchedTeacherSite{}, ReasonCounts: map[string]int64{}}
	if err := s.teacherRefreshAccess(ctx, p, school, "unmatched-viewed", p.CanReadTeacherSites); err != nil {
		return out, err
	}
	if reason != "" && !slices.Contains(TeacherUnmatchedReasons, reason) || limit < 1 || limit > 100 || len(after) > 4096 {
		return out, ErrInput
	}
	cursor := unmatchedCursor{}
	if after != "" {
		raw, e := base64.RawURLEncoding.Strict().DecodeString(after)
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.School != school || cursor.Reason != reason || !ValidID(cursor.ID) || cursor.Revision < 1 {
			return out, ErrInput
		}
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, e := refreshPort(t)
		if e != nil {
			return e
		}
		if e = port.BeginTeacherRefreshSnapshot(); e != nil {
			return e
		}
		out, e = port.TeacherUnmatched(school, reason, cursor.ID, limit+1)
		if e != nil {
			return e
		}
		if after != "" && cursor.Revision != out.Revision {
			return ErrConflict
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			raw, e := json.Marshal(unmatchedCursor{school, reason, out.Items[limit-1].SiteID, out.Revision})
			if e != nil {
				return ErrUnavailable
			}
			out.Next = base64.RawURLEncoding.EncodeToString(raw)
		}
		return s.teacherRefreshAudit(t, p, school, "unmatched-viewed", "school/"+school, "success")
	})
	return out, err
}
