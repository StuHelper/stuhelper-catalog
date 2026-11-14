package catalog

import (
	"errors"
	"slices"
)

func importEnrollment(t Transaction, en Enrollment, in Import) error {
	return importEnrollmentState(t, en, in, false)
}

func importEnrollmentState(t Transaction, en Enrollment, in Import, reactivate bool) error {
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
			if digest(old) == digest(en) {
				return nil
			}
			if !reactivate || old.Active || !en.Active {
				return ErrConflict
			}
			old.Active = true
			if digest(old) != digest(en) {
				return ErrConflict
			}
		}
	}
	if e := t.SaveEnrollment(en); e != nil {
		return e
	}
	return nil
}
