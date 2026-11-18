package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type CourseIdentityMapping struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
}

type CourseIdentities struct {
	SchoolID string                  `json:"school_id"`
	SourceID string                  `json:"source_id"`
	Revision int64                   `json:"revision"`
	Mappings []CourseIdentityMapping `json:"mappings"`
}

type CourseIdentitiesResult struct {
	ID        string `json:"id"`
	Mappings  int    `json:"mappings"`
	Redirects int    `json:"redirects"`
}

type CourseIdentityTransaction interface {
	CourseIdentityRecords([]string) ([]Record, map[string]string, error)
	RedirectCourses([]CourseIdentityMapping, time.Time, string) error
}

// ApplyCourseIdentities accepts only a source's explicit, complete one-to-one
// mappings. Original records and foreign keys remain intact, including writes
// after cutover. Rollback keeps this additive identity state, never restores a
// database snapshot over newer user content.
func (s *Service) ApplyCourseIdentities(ctx context.Context, p Principal, key string, in CourseIdentities) (CourseIdentitiesResult, error) {
	ctx = teacherMaintenanceContext(ctx)
	var out CourseIdentitiesResult
	if !p.CanImport || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if !ValidKey(key) || !ValidID(in.SchoolID) || !ValidID(in.SourceID) || in.Revision < 1 || len(in.Mappings) < 1 || len(in.Mappings) > 5000 {
		return out, ErrInput
	}
	sources, targets := map[string]bool{}, map[string]bool{}
	ids := make([]string, 0, len(in.Mappings)*2)
	for _, pair := range in.Mappings {
		if !ValidID(pair.SourceID) || !ValidID(pair.TargetID) || pair.SourceID == pair.TargetID || sources[pair.SourceID] || targets[pair.TargetID] {
			return out, ErrInput
		}
		sources[pair.SourceID], targets[pair.TargetID] = true, true
		ids = append(ids, pair.SourceID, pair.TargetID)
	}
	for id := range sources {
		if targets[id] {
			return out, ErrInput
		}
	}
	rk, hash := digest([]string{p.Subject, "course-identities", in.SchoolID, key}), digest(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		if err := t.Lock(in.SchoolID); err != nil {
			return err
		}
		cached, err := t.Replay(rk)
		if err == nil {
			if cached.Digest != hash {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &out)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		port, ok := t.(CourseIdentityTransaction)
		if !ok {
			return ErrUnavailable
		}
		records, redirects, err := port.CourseIdentityRecords(ids)
		if err != nil {
			return err
		}
		byID := make(map[string]Record, len(records))
		for _, rec := range records {
			byID[rec.Object.ID] = rec
		}
		changed := []CourseIdentityMapping{}
		incoming := map[string]string{}
		for alias, canonical := range redirects {
			if incoming[canonical] != "" {
				return ErrConflict
			}
			incoming[canonical] = alias
		}
		for _, pair := range in.Mappings {
			for _, id := range []string{pair.SourceID, pair.TargetID} {
				rec, exists := byID[id]
				if !exists || rec.Object.Kind != "course" || rec.Object.SchoolID != in.SchoolID || rec.SourceID != in.SourceID || rec.SourceRevision != in.Revision || rec.Object.Inactive {
					return ErrConflict
				}
			}
			if incoming[pair.SourceID] != "" || (incoming[pair.TargetID] != "" && incoming[pair.TargetID] != pair.SourceID) || redirects[pair.TargetID] != "" || (redirects[pair.SourceID] != "" && redirects[pair.SourceID] != pair.TargetID) {
				return ErrConflict
			}
			if redirects[pair.SourceID] == "" {
				changed = append(changed, pair)
			}
		}
		now := s.now()
		if len(changed) > 0 {
			if err := port.RedirectCourses(changed, now, "course-identities:"+in.SourceID); err != nil {
				return err
			}
			for _, pair := range changed {
				source, target := byID[pair.SourceID], byID[pair.TargetID]
				if err := addFormer(&target.Object, source.Object); err != nil {
					return err
				}
				for _, rec := range []*Record{&source, &target} {
					rec.Object.Revision++
					rec.Object.UpdatedAt = now
					if err := t.SaveObject(*rec); err != nil {
						return err
					}
				}
			}
		}
		relations, err := t.MergeAliases("", "", in.SchoolID, now, "Course identity relation")
		if err != nil {
			return err
		}
		out = CourseIdentitiesResult{ID: rk, Mappings: len(in.Mappings), Redirects: len(changed) + relations}
		if err := t.Event(Event{Result: "success", ID: rk, Subject: in.SchoolID, Type: "catalog.course_identities.v1", Reason: "catalog.course_identities", SchoolID: in.SchoolID, Actor: p.Subject, Revision: in.Revision, Time: now}); err != nil {
			return err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return t.SaveReplay(rk, Replay{Digest: hash, Result: data})
	})
	return out, err
}

func addFormer(target *Object, source Object) error {
	for _, code := range append(slices.Clone(source.FormerCodes), source.Code) {
		if code != "" && code != target.Code && !slices.Contains(target.FormerCodes, code) {
			target.FormerCodes = append(target.FormerCodes, code)
		}
	}
	for _, name := range append(slices.Clone(source.FormerNames), source.Name) {
		if name != "" && name != target.Name && !slices.Contains(target.FormerNames, name) {
			target.FormerNames = append(target.FormerNames, name)
		}
	}
	if len(target.FormerCodes) > 50 || len(target.FormerNames) > 50 {
		return ErrInput
	}
	return nil
}

func preserveCourseNames(rec *Record, old Record, separate bool) error {
	for _, code := range old.Object.FormerCodes {
		if code != rec.Object.Code && !slices.Contains(rec.Object.FormerCodes, code) {
			rec.Object.FormerCodes = append(rec.Object.FormerCodes, code)
		}
	}
	for _, name := range old.Object.FormerNames {
		if name != rec.Object.Name && !slices.Contains(rec.Object.FormerNames, name) {
			rec.Object.FormerNames = append(rec.Object.FormerNames, name)
		}
	}
	if separate {
		if len(rec.Object.Aliases) > 0 {
			return ErrInput
		}
		for _, alias := range old.Object.Aliases {
			// The importer explicitly declares the old source aliases to be
			// English names. Names added by maintenance remain searchable.
			if !slices.Contains(old.Imported.Aliases, alias) && alias != rec.Object.Name && alias != rec.Object.EnglishName && !slices.Contains(rec.Object.FormerNames, alias) {
				rec.Object.FormerNames = append(rec.Object.FormerNames, alias)
			}
		}
		rec.Object.Aliases = []string{}
	}
	if old.Object.Name != "" && old.Object.Name != rec.Object.Name && !slices.Contains(rec.Object.FormerNames, old.Object.Name) {
		rec.Object.FormerNames = append(rec.Object.FormerNames, old.Object.Name)
	}
	if len(rec.Object.FormerCodes) > 50 || len(rec.Object.FormerNames) > 50 {
		return ErrInput
	}
	return nil
}
