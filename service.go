package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Service struct {
	Repo  Repository
	Clock func() time.Time
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

// Import commits a bounded page atomically. Omitted rows are never interpreted as deletions.
func (s *Service) Import(ctx context.Context, p Principal, key string, in Import) (ImportResult, error) {
	var out ImportResult
	if !p.CanImport || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if !ValidKey(key) || !ValidID(in.SchoolID) || !ValidID(in.SourceID) || in.Revision < 1 || in.FetchedAt.IsZero() || in.FetchedAt.After(s.now().Add(time.Minute)) || len(in.Objects)+len(in.Enrollments) < 1 || len(in.Objects)+len(in.Enrollments) > 500 {
		return out, ErrInput
	}
	seen := map[string]bool{}
	for _, o := range in.Objects {
		if validateObject(o, in.SchoolID) != nil || seen[o.ID] {
			return out, ErrInput
		}
		seen[o.ID] = true
	}
	h := digest(in)
	rk := digest([]string{p.Subject, "import", in.SchoolID, key})
	now := s.now()
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(in.SchoolID); e != nil {
			return e
		}
		r, e := t.Replay(rk)
		if e == nil && now.Before(r.Until) {
			if r.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(r.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		out = ImportResult{ID: rk, Objects: len(in.Objects), Enrollments: len(in.Enrollments)}
		// Resolve within a page in dependency order, independent of input order.
		for _, kind := range kinds {
			for _, o := range in.Objects {
				if o.Kind != kind {
					continue
				}
				if e := relations(t, o); e != nil {
					return e
				}
				old, e := t.Object(o.ID)
				if e != nil && !errors.Is(e, ErrNotFound) {
					return e
				}
				imported := o
				imported.Revision = 0
				imported.UpdatedAt = time.Time{}
				if e == nil {
					if old.SourceID != in.SourceID || old.Object.Kind != o.Kind || old.Object.SchoolID != o.SchoolID {
						return ErrConflict
					}
					if old.SourceRevision > in.Revision {
						return ErrConflict
					}
					if old.SourceRevision == in.Revision {
						if digest(old.Imported) != digest(imported) {
							return ErrConflict
						}
						continue
					}
				}
				rec := Record{Object: o, Imported: imported, SourceID: in.SourceID, SourceRevision: in.Revision, Manual: old.Manual}
				rec.Object.Revision = old.Object.Revision + 1
				rec.Object.UpdatedAt = now
				for _, alias := range old.Object.Aliases {
					if !slices.Contains(rec.Object.Aliases, alias) && len(rec.Object.Aliases) < 50 {
						rec.Object.Aliases = append(rec.Object.Aliases, alias)
					}
				}
				if old.Object.Name != "" && old.Object.Name != o.Name && !slices.Contains(rec.Object.Aliases, old.Object.Name) && len(rec.Object.Aliases) < 50 {
					rec.Object.Aliases = append(rec.Object.Aliases, old.Object.Name)
				}
				if rec.Object.Aliases == nil {
					rec.Object.Aliases = []string{}
				}
				if e := applyManual(&rec); e != nil {
					return e
				}
				out.PreservedCorrections += len(rec.Manual)
				if e := relations(t, rec.Object); e != nil {
					return e
				}
				if e := t.SaveObject(rec); e != nil {
					return e
				}
			}
		}
		for _, en := range in.Enrollments {
			if !ValidID(en.ID) || en.Subject == "" || len(en.Subject) > 512 || en.SchoolID != in.SchoolID || len(en.TeacherIDs) > 100 {
				return ErrInput
			}
			for id, kind := range map[string]string{en.CourseID: "course", en.TermID: "term"} {
				r, e := t.Object(id)
				if e != nil {
					return e
				}
				if r.Object.Kind != kind || r.Object.SchoolID != in.SchoolID {
					return ErrInput
				}
			}
			if en.ClassID != "" {
				cl, e := t.Object(en.ClassID)
				if e != nil {
					return e
				}
				tc, e := t.Object(cl.Object.TermCourseID)
				if e != nil {
					return e
				}
				if cl.Object.Kind != "class" || tc.Object.CourseID != en.CourseID || tc.Object.TermID != en.TermID {
					return ErrInput
				}
			}
			slices.Sort(en.TeacherIDs)
			en.TeacherIDs = slices.Compact(en.TeacherIDs)
			for _, id := range en.TeacherIDs {
				r, e := t.Object(id)
				if e != nil {
					return e
				}
				if r.Object.Kind != "teacher" || r.Object.SchoolID != in.SchoolID {
					return ErrInput
				}
				if en.ClassID != "" {
					matched, err := t.HasAssignment(en.ClassID, id)
					if err != nil {
						return err
					}
					if !matched {
						return ErrInput
					}
				}
			}
			en.SourceID = in.SourceID
			en.SourceRevision = in.Revision
			en.FetchedAt = in.FetchedAt
			old, e := t.Enrollment(en.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				if old.SourceID != in.SourceID || old.Subject != en.Subject || old.SchoolID != en.SchoolID || old.SourceRevision > in.Revision {
					return ErrConflict
				}
				if old.SourceRevision == in.Revision {
					if digest(old) != digest(en) {
						return ErrConflict
					}
					continue
				}
			}
			if e := t.SaveEnrollment(en); e != nil {
				return e
			}
		}
		if e := t.Event(Event{ID: rk, Subject: "import/" + rk, Type: "catalog.imported.v1", SchoolID: in.SchoolID, Actor: p.Subject, Revision: in.Revision, Time: now}); e != nil {
			return e
		}
		b, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return t.SaveReplay(rk, Replay{Digest: h, Result: b, Until: now.Add(24 * time.Hour)})
	})
	return out, err
}
func (s *Service) Get(ctx context.Context, id string) (Object, error) {
	var out Object
	if !ValidID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error { r, e := t.Object(id); out = r.Object; return e })
	return out, err
}
func (s *Service) List(ctx context.Context, f Filter) (Page, error) {
	out := Page{Items: []Object{}}
	if !slices.Contains(kinds, f.Kind) || f.Limit < 1 || f.Limit > 100 || !validText(f.Query, 200) {
		return out, ErrInput
	}
	for _, id := range []string{f.SchoolID, f.DepartmentID, f.CategoryID, f.NatureID, f.CourseID, f.TermID, f.TeacherID, f.TermCourseID, f.ClassID, f.After} {
		if id != "" && !ValidID(id) {
			return out, ErrInput
		}
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
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
	var out []Enrollment
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		out, e = t.Enrollments(p.Subject, school, course, term, after, limit)
		return e
	})
	return out, err
}
func (s *Service) Correct(ctx context.Context, p Principal, id, key string, in Correction) (Object, error) {
	var out Object
	if !ValidID(id) || !ValidKey(key) || in.Revision < 1 || (in.Name == nil && in.Biography == nil && in.DepartmentID == nil && in.CategoryID == nil && in.NatureID == nil && in.Code == nil && in.Credits == nil && !in.ClearCredits) || (in.Credits != nil && in.ClearCredits) {
		return out, ErrInput
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || !validText(*in.Name, 256)) {
		return out, ErrInput
	}
	if in.Biography != nil && !validText(*in.Biography, 8000) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		r, e := t.Object(id)
		if e != nil {
			return e
		}
		if !p.CanMaintain || !allowed(p, r.Object.SchoolID) {
			return ErrForbidden
		}
		if e = t.Lock(r.Object.SchoolID); e != nil {
			return e
		}
		r, e = t.Object(id)
		if e != nil {
			return e
		}
		rk := digest([]string{p.Subject, "correct", id, key})
		h := digest(in)
		replay, e := t.Replay(rk)
		if e == nil && s.now().Before(replay.Until) {
			if replay.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(replay.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if r.Object.Revision != in.Revision {
			return ErrConflict
		}
		if (in.Credits != nil || in.ClearCredits || in.CategoryID != nil || in.NatureID != nil) && r.Object.Kind != "course" {
			return ErrInput
		}
		if e = recordCorrection(&r, in); e != nil {
			return e
		}
		if e = validateObject(r.Object, r.Object.SchoolID); e != nil {
			return e
		}
		if e = relations(t, r.Object); e != nil {
			return e
		}
		r.Object.Revision++
		r.Object.UpdatedAt = s.now()
		if e = t.SaveObject(r); e != nil {
			return e
		}
		out = r.Object
		if e = t.Event(Event{ID: rk, Subject: fmt.Sprintf("object/%s", id), Type: "catalog.corrected.v1", SchoolID: r.Object.SchoolID, Actor: p.Subject, Revision: r.Object.Revision, Time: s.now()}); e != nil {
			return e
		}
		b, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return t.SaveReplay(rk, Replay{Digest: h, Result: b, Until: s.now().Add(24 * time.Hour)})
	})
	return out, err
}
