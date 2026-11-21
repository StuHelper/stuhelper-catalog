package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

type teacherMaintenanceKey struct{}

func teacherMaintenanceContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, teacherMaintenanceKey{}, true)
}

// TeacherMaintenanceRead is consumed by Repository after Service has selected
// an authorized maintenance operation. HTTP callers cannot set this context key.
func TeacherMaintenanceRead(ctx context.Context) bool {
	value, _ := ctx.Value(teacherMaintenanceKey{}).(bool)
	return value
}

// TeacherCandidate is an authorized maintenance projection. Source staff numbers
// stay in the school adapter; consumers match only the name and owning unit.
type TeacherCandidate struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DepartmentID string `json:"department_id"`
	Unit         string `json:"unit"`
	Title        string `json:"title"`
	Teaching     bool   `json:"teaching"`
}

type TeacherSiteMatch struct {
	TeacherID    string `json:"teacher_id"`
	Name         string `json:"name"`
	DepartmentID string `json:"department_id"`
	URL          string `json:"url"`
}

// TeacherSiteSnapshot replaces one complete, verified website membership set.
// An interrupted crawl must not be submitted as a snapshot. Revision is the
// observation's Unix microsecond timestamp, not a local import attempt counter.
type TeacherSiteSnapshot struct {
	CandidateFingerprint string                                        `json:"candidate_fingerprint"`
	SchoolID             string                                        `json:"school_id"`
	SourceID             string                                        `json:"source_id"`
	Revision             int64                                         `json:"revision"`
	ObservedAt           time.Time                                     `json:"observed_at"`
	Matches              []TeacherSiteMatch                            `json:"matches"`
	Version              int                                           `json:"version,omitempty"`
	Sites                []TeacherSite                                 `json:"sites,omitempty"`
	Teachers             []TeacherSiteTeacher                          `json:"teachers,omitempty"`
	ReadPhoto            func(context.Context, string) ([]byte, error) `json:"-"`
}

type TeacherDirectoryResult struct {
	Teaching int `json:"teaching"`
	Official int `json:"official"`
	Both     int `json:"both"`
	Eligible int `json:"eligible"`
}

// TeacherDirectoryTransaction is implemented by the owning repository. It is
// separate from the ordinary read port because callers must hold import scope.
type TeacherDirectoryTransaction interface {
	TeacherCandidates(string, string, int) ([]TeacherCandidate, error)
	RefreshTeachingDirectory(string, time.Time) (TeacherDirectoryResult, bool, error)
	ReplaceTeacherSites(TeacherSiteSnapshot, string, time.Time) (TeacherDirectoryResult, error)
}

// TeacherHomepagePublicationTransaction records approval of verified teacher
// material in the same transaction as the request decision and audit entry.
// The caller holds the school lock and has checked the submitter's ownership.
type TeacherHomepagePublicationTransaction interface {
	GrantVerifiedTeacherHomepage(school, teacherID string, at time.Time) error
}

// TeacherCandidateFingerprint binds matching decisions to all candidate names
// and units, including competing homonyms. Titles and teaching changes do not
// affect this identity check. Repository recomputes it while holding the school
// write lock so a concurrently inserted homonym cannot make a match ambiguous.
func TeacherCandidateFingerprint(candidates []TeacherCandidate) string {
	rows := slices.Clone(candidates)
	slices.SortFunc(rows, func(a, b TeacherCandidate) int { return strings.Compare(a.ID, b.ID) })
	h := sha256.New()
	e := json.NewEncoder(h)
	for _, row := range rows {
		_ = e.Encode([4]string{row.ID, row.Name, row.DepartmentID, row.Unit})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) TeacherCandidates(ctx context.Context, p Principal, school, after string, limit int) ([]TeacherCandidate, error) {
	if !p.CanImport || p.Subject == "" || !allowed(p, school) {
		return nil, ErrForbidden
	}
	if !ValidID(school) || after != "" && !ValidID(after) || limit < 1 || limit > 500 {
		return nil, ErrInput
	}
	var out []TeacherCandidate
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(TeacherDirectoryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var e error
		out, e = port.TeacherCandidates(school, after, limit)
		return e
	})
	return out, err
}

func (s *Service) RefreshTeachingDirectory(ctx context.Context, p Principal, school string) (TeacherDirectoryResult, error) {
	var out TeacherDirectoryResult
	if !p.CanImport || p.Subject == "" || !allowed(p, school) {
		return out, ErrForbidden
	}
	if !ValidID(school) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(school); e != nil {
			return e
		}
		port, ok := t.(TeacherDirectoryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var e error
		var changed bool
		out, changed, e = port.RefreshTeachingDirectory(school, s.now())
		if e != nil {
			return e
		}
		if projection, ok := t.(DirectoryProjectionTransaction); ok {
			report, e := projection.RebuildPublishedDirectory(school, s.now())
			if e != nil {
				return e
			}
			if report != nil {
				out = report.Marks
			}
		}
		if !changed {
			return nil
		}
		return t.Event(Event{ID: "teacher-directory:" + digest([]any{school, s.now(), p.Subject}), Subject: "school/" + school,
			Type: "catalog.teacher-directory.refreshed.v1", SchoolID: school, Actor: p.Subject, Result: "success", Time: s.now(), Revision: s.now().UnixMicro()})
	})
	return out, err
}

func (s *Service) ImportTeacherSites(ctx context.Context, p Principal, in TeacherSiteSnapshot) (TeacherDirectoryResult, error) {
	var out TeacherDirectoryResult
	if !p.CanImport || p.Subject == "" || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if err := s.validateTeacherSites(&in); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(in.SchoolID); e != nil {
			return e
		}
		port, ok := t.(TeacherDirectoryTransaction)
		if !ok {
			return ErrUnavailable
		}
		now := s.now()
		if in.Sites != nil {
			if _, _, err := port.RefreshTeachingDirectory(in.SchoolID, now); err != nil {
				return err
			}
		}
		var err error
		out, err = port.ReplaceTeacherSites(in, p.Subject, now)
		return err
	})
	return out, err
}

func (s *Service) allowedTeacherSiteHosts(school string) []string {
	if len(s.TeacherSiteHosts) > 0 {
		return s.TeacherSiteHosts[school]
	}
	return []string{"shi.buaa.edu.cn"}
}

func validTeacherSiteHost(host string, allowed []string) bool {
	if host == "" {
		return false
	}
	for _, item := range allowed {
		candidate := strings.TrimSpace(item)
		if candidate != "" && strings.EqualFold(host, candidate) {
			return true
		}
	}
	return false
}

func validTeacherSiteURL(raw string, hosts []string) bool {
	if !validText(raw, 512) || raw != strings.TrimSpace(raw) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Opaque == "" && validTeacherSiteHost(u.Host, hosts) && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && validText(u.Path, 512) &&
		(u.RawPath == "" || u.RawPath == u.Path) && strings.HasPrefix(u.Path, "/") && path.Clean(u.Path) == u.Path &&
		u.Path != "/index.htm" && strings.HasSuffix(u.Path, "/index.htm")
}

func (s *Service) validateTeacherSites(in *TeacherSiteSnapshot) error {
	fingerprint, fingerprintError := hex.DecodeString(in.CandidateFingerprint)
	if fingerprintError != nil || len(fingerprint) != 32 || !ValidID(in.SchoolID) || !ValidID(in.SourceID) || in.Revision < 1 || in.ObservedAt.IsZero() ||
		in.ObservedAt.After(s.now().Add(5*time.Minute)) || len(in.Matches) > 20000 {
		return ErrInput
	}
	hosts := s.allowedTeacherSiteHosts(in.SchoolID)
	in.Matches = append([]TeacherSiteMatch{}, in.Matches...)
	slices.SortFunc(in.Matches, func(a, b TeacherSiteMatch) int { return strings.Compare(a.TeacherID, b.TeacherID) })
	for i, match := range in.Matches {
		if !ValidID(match.TeacherID) || !ValidID(match.DepartmentID) || !validText(match.Name, 256) || strings.TrimSpace(match.Name) == "" ||
			!validTeacherSiteURL(match.URL, hosts) || i > 0 && in.Matches[i-1].TeacherID == match.TeacherID {
			return ErrInput
		}
	}
	return validateTeacherHomepageSnapshot(in, hosts)
}
