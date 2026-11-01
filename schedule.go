package catalog

import (
	"context"
	"slices"
)

type ScheduleMeeting struct{ ID, TimeText, Location string }
type EnrollmentMeeting struct {
	EnrollmentID string
	Meeting      ScheduleMeeting
}
type OwnSchedule struct {
	Enrollment Enrollment
	CourseName string
	Meetings   []ScheduleMeeting
}
type OwnSchedulePage struct {
	Items []OwnSchedule
	Next  string
}

// MySchedule projects only active records owned by the authenticated account.
// It never reads general directory details or another student's enrollment.
func (s *Service) MySchedule(ctx context.Context, p Principal, term, after string, limit int) (OwnSchedulePage, error) {
	out := OwnSchedulePage{Items: []OwnSchedule{}}
	if p.Subject == "" {
		return out, ErrUnauthenticated
	}
	if limit < 1 || limit > 100 || (term != "" && !ValidID(term)) || (after != "" && !ValidID(after)) {
		return out, ErrInput
	}
	e := s.Repo.Transaction(ctx, func(tx Transaction) error {
		rows, e := tx.Enrollments(p.Subject, "", "", term, after, limit)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		ids, courses := []string{}, []string{}
		for _, v := range rows {
			ids = append(ids, v.ID)
			courses = append(courses, v.CourseID)
		}
		slices.Sort(courses)
		courses = slices.Compact(courses)
		names, e := tx.ReferenceNames(courses)
		if e != nil {
			return e
		}
		byName := map[string]string{}
		for _, v := range names {
			byName[v.ID] = v.Name
		}
		meetings, e := tx.OwnScheduleMeetings(p.Subject, ids, 1001)
		if e != nil {
			return e
		}
		if len(meetings) > 1000 {
			return ErrUnavailable
		}
		byEnrollment := map[string][]ScheduleMeeting{}
		for _, v := range meetings {
			byEnrollment[v.EnrollmentID] = append(byEnrollment[v.EnrollmentID], v.Meeting)
			if len(byEnrollment[v.EnrollmentID]) > 100 {
				return ErrUnavailable
			}
		}
		for _, v := range rows {
			items := byEnrollment[v.ID]
			if items == nil {
				items = []ScheduleMeeting{}
			}
			out.Items = append(out.Items, OwnSchedule{Enrollment: v, CourseName: byName[v.CourseID], Meetings: items})
		}
		if len(rows) == limit {
			out.Next = rows[len(rows)-1].ID
		}
		return nil
	})
	return out, e
}
