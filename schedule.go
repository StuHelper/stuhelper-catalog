package catalog

import (
	"context"
	"errors"
	"slices"
	"time"
)

type ScheduleMeeting struct {
	ID           string        `json:"id"`
	TimeText     string        `json:"time_text"`
	Location     string        `json:"location"`
	ScheduleTime *ScheduleTime `json:"schedule_time,omitempty"`
}
type EnrollmentMeeting struct {
	EnrollmentID string
	Meeting      ScheduleMeeting
}
type OwnSchedule struct {
	Enrollment Enrollment        `json:"enrollment"`
	CourseName string            `json:"course_name"`
	Meetings   []ScheduleMeeting `json:"meetings"`
	Timetable  *Timetable        `json:"timetable,omitempty"`
}
type OwnSchedulePage struct {
	Items []OwnSchedule `json:"items"`
	Next  string        `json:"next"`
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
	if err := s.refreshOwn(ctx, p); err != nil {
		return out, err
	}
	return s.ownSchedulePage(ctx, p, "", term, after, limit, false)
}

func (s *Service) ownSchedulePage(ctx context.Context, p Principal, school, term, after string, limit int, probeExtra bool) (OwnSchedulePage, error) {
	out := OwnSchedulePage{Items: []OwnSchedule{}}
	queryLimit := limit
	if probeExtra {
		queryLimit = limit + 1
	}
	e := s.Repo.Transaction(ctx, func(tx Transaction) error {
		started := time.Now()
		rows, e := tx.Enrollments(p.Subject, school, "", term, after, queryLimit)
		s.observeOwn(ctx, "schedule.enrollments", started, e)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		hasMore := false
		if probeExtra {
			if len(rows) > limit {
				hasMore = true
				rows = rows[:limit]
			}
		} else if len(rows) == limit {
			hasMore = true
		}
		ids, courses := []string{}, []string{}
		for _, v := range rows {
			ids = append(ids, v.ID)
			courses = append(courses, v.CourseID)
		}
		slices.Sort(courses)
		courses = slices.Compact(courses)
		started = time.Now()
		names, e := tx.ReferenceNames(courses)
		s.observeOwn(ctx, "schedule.names", started, e)
		if e != nil {
			return e
		}
		byName := map[string]string{}
		for _, v := range names {
			byName[v.ID] = v.Name
		}
		started = time.Now()
		meetings, e := tx.OwnScheduleMeetings(p.Subject, ids, 1001)
		s.observeOwn(ctx, "schedule.meetings", started, e)
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
		configs := map[string]*Timetable{}
		for _, v := range rows {
			key := v.SchoolID + "/" + v.TermID
			if _, ok := configs[key]; ok {
				continue
			}
			started = time.Now()
			c, e := tx.Timetable(v.SchoolID, v.TermID)
			diagnostic := e
			if errors.Is(e, ErrNotFound) {
				diagnostic = nil
			}
			s.observeOwn(ctx, "schedule.timetable", started, diagnostic)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			configs[key] = nil
			if e == nil {
				configs[key] = &c
			}
		}
		for _, v := range rows {
			items := byEnrollment[v.ID]
			if items == nil {
				items = []ScheduleMeeting{}
			}
			out.Items = append(out.Items, OwnSchedule{Enrollment: v, CourseName: byName[v.CourseID], Meetings: items, Timetable: configs[v.SchoolID+"/"+v.TermID]})
		}
		if hasMore {
			out.Next = rows[len(rows)-1].ID
		}
		return nil
	})
	return out, e
}
