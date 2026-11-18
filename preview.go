package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
)

type ImportChange struct {
	ScheduleTime *ScheduleTime `json:"schedule_time,omitempty"`
	TimeText     string        `json:"time_text,omitempty"`
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	State        string        `json:"state"`
	Fields       []string      `json:"fields"`
}
type ImportPreview struct {
	Valid        bool           `json:"valid"`
	Code         string         `json:"code"`
	PreviewToken string         `json:"preview_token"`
	Changes      []ImportChange `json:"changes"`
}

var rollbackPreview = errors.New("catalog_preview_rollback")

type previewRepository struct{ repo Repository }

func (p previewRepository) Transaction(ctx context.Context, run func(Transaction) error) error {
	err := p.repo.Transaction(ctx, func(t Transaction) error {
		if err := run(t); err != nil {
			return err
		}
		return rollbackPreview
	})
	if errors.Is(err, rollbackPreview) {
		return nil
	}
	return err
}
func importToken(t Transaction, in Import) (string, error) {
	in.PreviewToken = ""
	in.Objects = normalizeSchedules(in.Objects)
	ids := []string{in.SchoolID}
	for _, o := range in.Objects {
		ids = append(ids, o.ID, o.DepartmentID, o.CategoryID, o.NatureID, o.CourseID, o.TermID, o.TermCourseID, o.ClassID, o.TeacherID)
	}
	for _, e := range in.Enrollments {
		ids = append(ids, e.CourseID, e.TermID, e.ClassID)
		ids = append(ids, e.TeacherIDs...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	versions := []any{in}
	for _, id := range ids {
		if id == "" {
			continue
		}
		record, e := t.Object(id)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return "", e
		}
		versions = append(versions, record)
		if e == nil {
			aliases, e := t.ResolveIDs(id)
			if e != nil {
				return "", e
			}
			versions = append(versions, aliases)
		}
	}
	for _, e := range in.Enrollments {
		old, err := t.Enrollment(e.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
		versions = append(versions, old)
	}
	return digest(versions), nil
}
func changedFields(a, b Object) []string {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	var x, y map[string]any
	_ = json.Unmarshal(left, &x)
	_ = json.Unmarshal(right, &y)
	out := []string{}
	for key := range x {
		if _, ok := y[key]; !ok {
			y[key] = nil
		}
	}
	for key, value := range y {
		if key == "revision" || key == "updated_at" {
			continue
		}
		if digest(x[key]) != digest(value) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}
func hasManualFieldOverlap(fields []string, manual map[string]string) bool {
	for _, field := range fields {
		if field == "inactive_reason" {
			continue
		}
		if _, ok := manual[field]; ok {
			return true
		}
	}
	return false
}

func (s *Service) PreviewImport(ctx context.Context, p Principal, in Import) (ImportPreview, error) {
	ctx = teacherMaintenanceContext(ctx)
	out := ImportPreview{Changes: []ImportChange{}}
	if !p.CanImport || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if !ValidID(in.SchoolID) || len(in.Objects)+len(in.Enrollments) < 1 || len(in.Objects)+len(in.Enrollments) > 500 {
		return out, ErrInput
	}
	now := s.now()
	in.PreviewToken = ""
	in.Objects = normalizeSchedules(in.Objects)
	err := (previewRepository{s.Repo}).Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(in.SchoolID); e != nil {
			return e
		}
		var e error
		out.PreviewToken, e = importToken(t, in)
		if e != nil {
			return e
		}
		incoming := map[string]Object{}
		for _, o := range in.Objects {
			incoming[o.ID] = o
		}
		for _, o := range in.Objects {
			change := ImportChange{ID: o.ID, Kind: o.Kind, ScheduleTime: o.ScheduleTime, TimeText: o.TimeText, State: "new", Fields: []string{}}
			old, e := t.Object(o.ID)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				change.State = "unchanged"
				change.Fields = changedFields(old.Imported, o)
				if len(change.Fields) > 0 {
					change.State = "updated"
					if hasManualFieldOverlap(change.Fields, old.Manual) {
						change.State = "preserved"
					}
				}
				if old.SourceID != in.SourceID || old.SourceRevision > in.Revision || (old.SourceRevision == in.Revision && len(change.Fields) > 0) || old.Object.Kind != o.Kind || old.Object.SchoolID != o.SchoolID {
					change.State = "conflict"
				}
			}
			for _, id := range []string{o.DepartmentID, o.CategoryID, o.NatureID, o.CourseID, o.TermID, o.TermCourseID, o.ClassID, o.TeacherID} {
				if id == "" {
					continue
				}
				if _, ok := incoming[id]; ok {
					continue
				}
				_, e := t.Object(id)
				if errors.Is(e, ErrNotFound) {
					change.State = "unlinked"
				} else if e != nil {
					return e
				}
			}
			out.Changes = append(out.Changes, change)
		}
		for _, e := range in.Enrollments {
			change := ImportChange{ID: e.ID, Kind: "enrollment", State: "new", Fields: []string{}}
			old, err := t.Enrollment(e.ID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if err == nil {
				normalized := e
				slices.Sort(normalized.TeacherIDs)
				normalized.TeacherIDs = slices.Compact(normalized.TeacherIDs)
				normalized.SourceID = in.SourceID
				normalized.SourceRevision = in.Revision
				normalized.FetchedAt = in.FetchedAt
				comparableOld := old
				comparableOld.SourceRevision = in.Revision
				comparableOld.FetchedAt = in.FetchedAt
				if digest(comparableOld) == digest(normalized) {
					change.State = "unchanged"
				} else {
					change.State = "updated"
				}
				if old.Subject != e.Subject || old.SchoolID != e.SchoolID || old.SourceID != in.SourceID || old.SourceRevision > in.Revision || (old.SourceRevision == in.Revision && digest(old) != digest(normalized)) {
					change.State = "conflict"
				}
			}
			out.Changes = append(out.Changes, change)
		}

		dryIn := in
		dryIn.Objects = slices.Clone(in.Objects)
		dryIn.Enrollments = slices.Clone(in.Enrollments)
		dryErr := validateImportBatch(&dryIn, now)
		if dryErr == nil {
			withoutToken := dryIn
			withoutToken.PreviewToken = ""
			h := digest(withoutToken)
			rk := digest([]string{p.Subject, "import", dryIn.SchoolID, "preview-" + out.PreviewToken})
			dryIn.PreviewToken = out.PreviewToken
			_, dryErr = applyImportTx(t, p, rk, h, dryIn, now)
		}
		switch {
		case dryErr == nil:
			out.Valid = true
		case errors.Is(dryErr, ErrConflict):
			out.Code = "conflict"
		case errors.Is(dryErr, ErrInput), errors.Is(dryErr, ErrNotFound):
			out.Code = "invalid_input"
		default:
			return dryErr
		}
		return nil
	})
	return out, err
}
