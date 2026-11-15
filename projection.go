package catalog

import (
	"context"
	"time"
)

// TermOrder is supplied by the school's adapter, never inferred from a label.
// Invalid source terms are retained with a fixed diagnostic reason and key zero.
type TermOrder struct {
	ID     string `json:"id"`
	Year   int    `json:"year"`
	Season string `json:"season"`
	Reason string `json:"reason,omitempty"`
}

func TermOrderKey(year int, season string) int {
	if year < 1900 || year > 2200 {
		return 0
	}
	switch season {
	case "spring":
		return year*10 + 1
	case "summer":
		return year*10 + 2
	case "autumn":
		return year*10 + 3
	default:
		return 0
	}
}

type DirectoryPolicy struct {
	CutoffYear   int    `json:"cutoff_year"`
	CutoffSeason string `json:"cutoff_season"`
}

func DefaultDirectoryPolicy() DirectoryPolicy { return DirectoryPolicy{2023, "autumn"} }
func (p DirectoryPolicy) Valid() bool         { return TermOrderKey(p.CutoffYear, p.CutoffSeason) > 0 }

type ProjectionReport struct {
	Version         int64                  `json:"version"`
	Policy          DirectoryPolicy        `json:"policy"`
	Objects         int64                  `json:"objects"`
	Courses         int64                  `json:"courses"`
	Teachers        int64                  `json:"teachers"`
	Variants        int64                  `json:"variants"`
	Relations       int64                  `json:"relations"`
	TeachingHistory int64                  `json:"teaching_history"`
	Summaries       int64                  `json:"summaries"`
	Documents       int64                  `json:"documents"`
	Aliases         int64                  `json:"aliases"`
	Selections      int64                  `json:"selections"`
	Rows            int64                  `json:"rows"`
	Options         int64                  `json:"options"`
	InvalidTerms    int64                  `json:"invalid_terms"`
	Bytes           int64                  `json:"bytes"`
	StorageBytes    int64                  `json:"storage_bytes"`
	DurationMS      int64                  `json:"duration_ms"`
	Marks           TeacherDirectoryResult `json:"marks"`
	BuiltAt         time.Time              `json:"built_at"`
}

type DirectoryProjectionTransaction interface {
	SaveTermOrders(string, []TermOrder) error
	RebuildDirectory(string, *DirectoryPolicy, time.Time) (ProjectionReport, error)
	RebuildPublishedDirectory(string, time.Time) (*ProjectionReport, error)
	DirectoryProjection(string) (ProjectionReport, error)
	BeginDirectoryImport(string, string, time.Time) error
	EndDirectoryImport(string, string, bool, time.Time) error
}

type directoryBatchKey struct{}
type directoryImportKey struct{}

func DirectoryImportID(ctx context.Context) string {
	v, _ := ctx.Value(directoryImportKey{}).(string)
	return v
}

// RunDirectoryImport keeps partial source pages away from every published read
// and configuration rebuild, including across process crashes. A retry may reuse
// the same source batch; publication and completion share the final transaction.
func (s *Service) RunDirectoryImport(ctx context.Context, p Principal, school, batch string, run func(context.Context) error) (ProjectionReport, error) {
	var out ProjectionReport
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	if !p.CanImport || !allowed(p, school) {
		return out, ErrForbidden
	}
	if !ValidID(school) || !ValidKey(batch) || run == nil {
		return out, ErrInput
	}
	ctx = context.WithValue(DeferDirectoryRebuild(ctx), directoryImportKey{}, batch)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		return port.BeginDirectoryImport(school, batch, s.now())
	})
	if err != nil {
		return out, err
	}
	err = run(ctx)
	if err == nil {
		err = s.Repo.Transaction(ctx, func(t Transaction) error {
			port, ok := t.(DirectoryProjectionTransaction)
			if !ok {
				return ErrUnavailable
			}
			var e error
			out, e = port.RebuildDirectory(school, nil, s.now())
			if e != nil {
				return e
			}
			return port.EndDirectoryImport(school, batch, true, s.now())
		})
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := s.Repo.Transaction(cleanup, func(t Transaction) error {
			port, ok := t.(DirectoryProjectionTransaction)
			if !ok {
				return ErrUnavailable
			}
			return port.EndDirectoryImport(school, batch, false, s.now())
		}); e != nil {
			return out, ErrUnavailable
		}
	}
	return out, err
}

func (s *Service) DirectoryProjection(ctx context.Context, p Principal, school string) (ProjectionReport, error) {
	var out ProjectionReport
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	if !p.CanImport || !allowed(p, school) {
		return out, ErrForbidden
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.DirectoryProjection(school)
		return err
	})
	return out, err
}

// DeferDirectoryRebuild is for a trusted full-source importer. It must call
// RebuildDirectory after every page has succeeded. HTTP callers cannot set it.
func DeferDirectoryRebuild(ctx context.Context) context.Context {
	return context.WithValue(ctx, directoryBatchKey{}, true)
}
func DirectoryRebuildDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(directoryBatchKey{}).(bool)
	return v
}

func (s *Service) SaveTermOrders(ctx context.Context, p Principal, school string, terms []TermOrder) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if !p.CanImport || !allowed(p, school) {
		return ErrForbidden
	}
	if !ValidID(school) || len(terms) > 500 {
		return ErrInput
	}
	seen := map[string]bool{}
	for _, term := range terms {
		if !ValidID(term.ID) || seen[term.ID] {
			return ErrInput
		}
		seen[term.ID] = true
		if term.Reason == "" {
			if TermOrderKey(term.Year, term.Season) == 0 {
				return ErrInput
			}
		} else {
			if term.Year != 0 || term.Season != "" {
				return ErrInput
			}
			switch term.Reason {
			case "invalid_term_code", "academic_year_not_consecutive", "unknown_semester", "missing_term_order":
			default:
				return ErrInput
			}
		}
	}
	return s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		return port.SaveTermOrders(school, terms)
	})
}

// A nil policy rebuilds using the currently published policy. Configuration
// changes pass a full policy and publish it together with the new generation.
func (s *Service) RebuildDirectory(ctx context.Context, p Principal, school string, policy *DirectoryPolicy) (ProjectionReport, error) {
	var out ProjectionReport
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	if !p.CanImport || !allowed(p, school) {
		return out, ErrForbidden
	}
	if !ValidID(school) || policy != nil && !policy.Valid() {
		return out, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.RebuildDirectory(school, policy, s.now())
		if err != nil {
			return err
		}
		return t.Event(Event{ID: "directory-projection:" + digest([]any{school, out.Version}), Subject: "school/" + school, Type: "catalog.directory.rebuilt.v1", SchoolID: school, Actor: p.Subject, Result: "success", Revision: out.Version, Time: s.now()})
	})
	return out, err
}
