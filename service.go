package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	DirectoryMetrics         DirectoryMetricProvider
	DirectoryConfig          DirectoryConfig
	RecentResources          RecentResourceCounts
	OwnSummaryAccess         OwnSummaryAccess
	EnrollmentSource         EnrollmentSource
	TeacherHomepageOwnership TeacherHomepageOwnership
	TeacherSiteHosts         map[string][]string
	ActivityLogger           *slog.Logger
	OwnLogger                *slog.Logger
	DirectoryLogger          *slog.Logger
	AsyncMaintenanceRebuild  bool
	viewStats                viewCounters
	ReadAccess               ContentAccess
	Repo                     Repository
	Clock                    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}
func ValidID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
			return false
		}
	}
	return true
}
func ValidKey(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
func allowed(p Principal, school string) bool {
	return p.Subject != "" && slices.Contains(p.Schools, school)
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validText(v string, n int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= n && !strings.ContainsRune(v, 0)
}

var kinds = []string{"school", "department", "category", "nature", "term", "course", "teacher", "term_course", "class", "assignment", "schedule"}

func validateObject(o Object, school string) error {
	if o.TeacherHomepage != nil {
		return ErrInput
	}
	if !validCourseFields(o) {
		return ErrInput
	}
	if o.TermOrderKey != 0 || o.LatestTermID != "" || o.CourseType != "" {
		return ErrInput
	}
	if !validText(o.SportsProjectCode, 64) || (o.SportsProjectCode != "" && (o.Kind != "class" || o.SportsProjectCode != strings.TrimSpace(o.SportsProjectCode))) {
		return ErrInput
	}
	if !validSchedule(o.ScheduleTime) || (o.ScheduleTime != nil && o.Kind != "schedule") {
		return ErrInput
	}
	if !ValidID(o.ID) || o.SchoolID != school || !slices.Contains(kinds, o.Kind) || strings.TrimSpace(o.Name) == "" || !validText(o.Name, 256) || !validText(o.Code, 128) || !validText(o.Biography, 8000) || !validText(o.TimeText, 1024) || !validText(o.Location, 512) || len(o.Aliases) > 50 {
		return ErrInput
	}
	for _, v := range []string{o.DepartmentID, o.CategoryID, o.NatureID, o.CourseID, o.TermID, o.TermCourseID, o.ClassID, o.TeacherID} {
		if v != "" && !ValidID(v) {
			return ErrInput
		}
	}
	for _, v := range o.Aliases {
		if !validText(v, 256) {
			return ErrInput
		}
	}
	if o.Credits != nil && (*o.Credits < 0 || *o.Credits > 100 || math.IsNaN(*o.Credits) || math.IsInf(*o.Credits, 0)) {
		return ErrInput
	}
	if o.Kind == "school" && o.ID != school {
		return ErrInput
	}
	return nil
}
func relations(t Transaction, o Object) error {
	refs := map[string]string{}
	if o.Kind != "school" {
		refs[o.SchoolID] = "school"
	}
	if o.DepartmentID != "" {
		refs[o.DepartmentID] = "department"
	}
	if o.CategoryID != "" {
		refs[o.CategoryID] = "category"
	}
	if o.NatureID != "" {
		refs[o.NatureID] = "nature"
	}
	switch o.Kind {
	case "term_course":
		refs[o.CourseID] = "course"
		refs[o.TermID] = "term"
	case "class":
		refs[o.TermCourseID] = "term_course"
	case "assignment":
		refs[o.ClassID] = "class"
		refs[o.TeacherID] = "teacher"
	case "schedule":
		refs[o.ClassID] = "class"
	}
	for id, kind := range refs {
		r, e := t.Object(id)
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				return ErrInput
			}
			return e
		}
		if r.Object.Kind != kind || r.Object.SchoolID != o.SchoolID {
			return ErrInput
		}
	}
	return nil
}

func validateImportBatch(in *Import, now time.Time) error {
	if !ValidID(in.SchoolID) || !ValidID(in.SourceID) || in.Revision < 1 || in.FetchedAt.IsZero() || in.FetchedAt.After(now.Add(time.Minute)) || len(in.Objects)+len(in.Enrollments) < 1 || len(in.Objects)+len(in.Enrollments) > 500 {
		return ErrInput
	}
	in.Objects = normalizeSchedules(in.Objects)
	seen := map[string]bool{}
	for _, o := range in.Objects {
		if validateObject(o, in.SchoolID) != nil || seen[o.ID] || (in.SeparateCourseNames && o.Kind == "course" && len(o.Aliases) != 0) {
			return ErrInput
		}
		seen[o.ID] = true
	}
	return nil
}

func applyImportTx(t Transaction, p Principal, rk, h string, in Import, now time.Time) (ImportResult, error) {
	var out ImportResult
	if in.PreviewToken != "" {
		token, e := importToken(t, in)
		if e != nil {
			return out, e
		}
		if token != in.PreviewToken {
			return out, ErrConflict
		}
	}
	out = ImportResult{ID: rk, Objects: len(in.Objects), Enrollments: len(in.Enrollments)}
	// Resolve within a page in dependency order, independent of input order.
	for _, kind := range kinds {
		for _, o := range in.Objects {
			if o.Kind != kind {
				continue
			}
			if e := relations(t, o); e != nil {
				return out, e
			}
			old, e := t.Object(o.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return out, e
			}
			imported := o
			imported.Revision = 0
			imported.UpdatedAt = time.Time{}
			if e == nil {
				if old.SourceID != in.SourceID || old.Object.Kind != o.Kind || old.Object.SchoolID != o.SchoolID {
					return out, ErrConflict
				}
				if old.SourceRevision > in.Revision {
					return out, ErrConflict
				}
				if old.SourceRevision == in.Revision {
					if digest(old.Imported) != digest(imported) {
						return out, ErrConflict
					}
					continue
				}
				if digest(old.Imported) == digest(imported) {
					// Refresh source provenance without creating another public
					// object/history/search revision for identical weekly data.
					if err := t.AdvanceSourceRevision(o.ID, in.Revision); err != nil {
						return out, err
					}
					out.PreservedCorrections += len(old.Manual)
					if _, ok := old.Manual["inactive_reason"]; ok {
						out.PreservedCorrections--
					}
					continue
				}
			}
			rec := Record{Object: o, Imported: imported, SourceID: in.SourceID, SourceRevision: in.Revision, Manual: old.Manual}
			rec.Object.Revision = old.Object.Revision + 1
			rec.Object.UpdatedAt = now
			if o.Kind == "course" {
				if err := preserveCourseNames(&rec, old, in.SeparateCourseNames); err != nil {
					return out, err
				}
			}
			if o.Kind != "course" || !in.SeparateCourseNames {
				for _, alias := range old.Object.Aliases {
					if !slices.Contains(rec.Object.Aliases, alias) && len(rec.Object.Aliases) < 50 {
						rec.Object.Aliases = append(rec.Object.Aliases, alias)
					}
				}
				if o.Kind != "course" && old.Object.Name != "" && old.Object.Name != o.Name && !slices.Contains(rec.Object.Aliases, old.Object.Name) && len(rec.Object.Aliases) < 50 {
					rec.Object.Aliases = append(rec.Object.Aliases, old.Object.Name)
				}
			}
			if rec.Object.Aliases == nil {
				rec.Object.Aliases = []string{}
			}
			if e := applyManual(&rec); e != nil {
				return out, e
			}
			out.PreservedCorrections += len(rec.Manual)
			if _, ok := rec.Manual["inactive_reason"]; ok {
				out.PreservedCorrections--
			}
			if e := relations(t, rec.Object); e != nil {
				return out, e
			}
			if e := t.SaveObject(rec); e != nil {
				return out, e
			}
		}
	}
	for _, en := range in.Enrollments {
		if e := importEnrollment(t, en, in); e != nil {
			return out, e
		}
	}
	if _, e := t.MergeAliases("", "", in.SchoolID, now, "Canonical relation after import"); e != nil {
		return out, e
	}
	if e := t.Event(Event{Result: "success", ID: rk, Subject: "import/" + rk, Type: "catalog.imported.v1", SchoolID: in.SchoolID, Actor: p.Subject, Revision: in.Revision, Time: now}); e != nil {
		return out, e
	}
	b, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	return out, t.SaveReplay(rk, Replay{Digest: h, Result: b})
}

// Import commits a bounded page atomically. Omitted rows are never interpreted as deletions.
func (s *Service) Import(ctx context.Context, p Principal, key string, in Import) (ImportResult, error) {
	ctx = teacherMaintenanceContext(ctx)
	var out ImportResult
	if !p.CanImport || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	now := s.now()
	if !ValidKey(key) || validateImportBatch(&in, now) != nil {
		return out, ErrInput
	}
	withoutToken := in
	withoutToken.PreviewToken = ""
	h := digest(withoutToken)
	rk := digest([]string{p.Subject, "import", in.SchoolID, key})
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(in.SchoolID); e != nil {
			return e
		}
		r, e := t.Replay(rk)
		if e == nil {
			if r.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(r.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		var err error
		out, err = applyImportTx(t, p, rk, h, in, now)
		return err
	})
	return out, err
}
func (s *Service) Get(ctx context.Context, id string) (Object, error) {
	var out Object
	if !ValidID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error { r, e := t.PublicObject(id); out = r; return e })
	return out, err
}
func (s *Service) List(ctx context.Context, f Filter) (page Page, err error) {
	start := time.Now()
	defer func() { s.observeDirectory(ctx, "list", start, err) }()
	f.Query = strings.TrimSpace(f.Query)
	if len(f.IDs) > 100 {
		return Page{}, ErrInput
	}
	for _, id := range f.IDs {
		if !ValidID(id) {
			return Page{}, ErrInput
		}
	}
	out := Page{Items: []Object{}}
	if !slices.Contains(kinds, f.Kind) || f.Limit < 1 || f.Limit > 100 || !validText(f.Query, 200) {
		return out, ErrInput
	}
	for _, id := range []string{f.SchoolID, f.DepartmentID, f.CategoryID, f.NatureID, f.CourseID, f.TermID, f.TeacherID, f.TermCourseID, f.ClassID, f.After} {
		if id != "" && !ValidID(id) {
			return out, ErrInput
		}
	}
	err = s.Repo.Transaction(ctx, func(t Transaction) error {
		for _, id := range []*string{&f.CourseID, &f.TeacherID, &f.TermCourseID, &f.ClassID} {
			if *id != "" {
				object, e := t.PublicObject(*id)
				if e != nil {
					return e
				}
				if object.CanonicalID != "" {
					*id = object.CanonicalID
				}
			}
		}
		items, e := t.List(f)
		if e != nil {
			return e
		}
		if len(items) > f.Limit {
			out.Next = items[f.Limit-1].ID
			items = items[:f.Limit]
		}
		out.Items = items
		return nil
	})
	return out, err
}
func (s *Service) MyEnrollments(ctx context.Context, p Principal, school, course, term, after string, limit int) ([]Enrollment, error) {
	if p.Subject == "" {
		return nil, ErrForbidden
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	for _, id := range []string{school, course, term, after} {
		if id != "" && !ValidID(id) {
			return nil, ErrInput
		}
	}
	if err := s.refreshOwn(ctx, p); err != nil {
		return nil, err
	}
	var out []Enrollment
	started := time.Now()
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		out, e = t.Enrollments(p.Subject, school, course, term, after, limit)
		return e
	})
	s.observeOwn(ctx, "enrollments.read", started, err)
	return out, err
}
func (s *Service) Correct(ctx context.Context, p Principal, id, key string, in Correction) (Object, error) {
	if err := maintainRead(p); err != nil {
		return Object{}, err
	}
	if !ValidID(id) || !ValidKey(key) || in.Revision < 1 || !validCorrectionReset(in) ||
		(in.Name == nil && in.Biography == nil && in.DepartmentID == nil && in.CategoryID == nil && in.NatureID == nil && in.Code == nil && in.Credits == nil && !in.ClearCredits && len(in.ResetFields) == 0) ||
		(in.Credits != nil && in.ClearCredits) {
		return Object{}, ErrInput
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || !validText(*in.Name, 256)) {
		return Object{}, ErrInput
	}
	if in.Biography != nil && !validText(*in.Biography, 8000) {
		return Object{}, ErrInput
	}
	return s.maintainWrite(ctx, p, id, "correct", key, in, func(t Transaction, r Record, rk string, now time.Time) (Object, Event, error) {
		if r.Object.Revision != in.Revision {
			return Object{}, Event{}, ErrConflict
		}
		courseReset := slices.Contains(in.ResetFields, "credits") || slices.Contains(in.ResetFields, "category_id") || slices.Contains(in.ResetFields, "nature_id")
		if (in.Credits != nil || in.ClearCredits || in.CategoryID != nil || in.NatureID != nil || courseReset) && r.Object.Kind != "course" {
			return Object{}, Event{}, ErrInput
		}
		if (in.Biography != nil || slices.Contains(in.ResetFields, "biography")) && !slices.Contains([]string{"course", "teacher"}, r.Object.Kind) {
			return Object{}, Event{}, ErrInput
		}
		if (in.DepartmentID != nil || slices.Contains(in.ResetFields, "department_id")) && !slices.Contains([]string{"course", "teacher", "term_course", "class"}, r.Object.Kind) {
			return Object{}, Event{}, ErrInput
		}
		if (in.Code != nil || slices.Contains(in.ResetFields, "code")) && !slices.Contains([]string{"school", "department", "category", "nature", "term", "course", "term_course", "class"}, r.Object.Kind) {
			return Object{}, Event{}, ErrInput
		}
		if e := recordCorrection(&r, in); e != nil {
			return Object{}, Event{}, e
		}
		if e := validateObject(r.Object, r.Object.SchoolID); e != nil {
			return Object{}, Event{}, e
		}
		if e := relations(t, r.Object); e != nil {
			return Object{}, Event{}, e
		}
		r.Object.Revision++
		r.Object.UpdatedAt = now
		if e := t.SaveObject(r); e != nil {
			return Object{}, Event{}, e
		}
		return r.Object, Event{Result: "success", ID: rk, Subject: fmt.Sprintf("object/%s", id), Type: "catalog.corrected.v1", SchoolID: r.Object.SchoolID, Actor: p.Subject, Revision: r.Object.Revision, Time: now}, nil
	})
}
