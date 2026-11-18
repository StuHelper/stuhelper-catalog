package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

type NextLesson struct {
	CourseID    string    `json:"course_id"`
	CourseName  string    `json:"course_name"`
	Teachers    []Object  `json:"teachers"`
	Location    string    `json:"location"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	StartPeriod int       `json:"start_period"`
	EndPeriod   int       `json:"end_period"`
}
type NextClassDay struct {
	State    string       `json:"state"`
	Reason   string       `json:"reason"`
	Date     *string      `json:"date"`
	TimeZone string       `json:"time_zone"`
	Items    []NextLesson `json:"items"`
}

// OwnSummaryAccess authorizes displaying enrollment-derived data. An enrollment
// source used for review eligibility does not by itself grant this permission.
// Applications leave this port unset while owner-facing course display is closed.
type OwnSummaryAccess interface {
	AuthorizeOwnSummary(context.Context, Principal, string) error
}

func (s *Service) authorizeOwnSummary(ctx context.Context, p Principal, school string) error {
	if s.OwnSummaryAccess == nil {
		return ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := s.OwnSummaryAccess.AuthorizeOwnSummary(callCtx, p, school)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return ErrUnavailable
	}
	return err
}

func timetableBounds(tt *Timetable) (start, end time.Time, ok bool) {
	if tt == nil {
		return time.Time{}, time.Time{}, false
	}
	loc, err := time.LoadLocation(tt.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	monday, err := time.ParseInLocation("2006-01-02", tt.WeekOneStart, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return monday, monday.AddDate(0, 0, 60*7), true
}

func termOrderUpperBound(orderKey int) (time.Time, bool) {
	if orderKey <= 0 {
		return time.Time{}, false
	}
	year, season := orderKey/10, orderKey%10
	switch season {
	case 1:
		return time.Date(year, 10, 1, 0, 0, 0, 0, time.UTC), true
	case 2:
		return time.Date(year, 11, 1, 0, 0, 0, 0, time.UTC), true
	case 3:
		return time.Date(year+1, 4, 1, 0, 0, 0, 0, time.UTC), true
	default:
		return time.Time{}, false
	}
}

func isHistoricalEndedTerm(row OwnSchedule, now time.Time, latestStartedTermKey int) bool {
	if _, termEnd, ok := timetableBounds(row.Timetable); ok && !termEnd.After(now) {
		return true
	}
	if row.Enrollment.TermOrderKey != nil && *row.Enrollment.TermOrderKey > 0 {
		key := *row.Enrollment.TermOrderKey
		if latestStartedTermKey > 0 && key < latestStartedTermKey {
			return true
		}
		if bound, ok := termOrderUpperBound(key); ok && !bound.After(now) {
			return true
		}
	}
	return false
}

// MyNextClassDay does not use cached enrollment rows without the current owner
// source. The same source enforces authorization for MySchedule and Evidence.
func (s *Service) MyNextClassDay(ctx context.Context, p Principal) (NextClassDay, error) {
	out := NextClassDay{State: "unavailable", Reason: "enrollment_unavailable", Items: []NextLesson{}}
	school, err := s.readScope(ctx, p, "")
	if err != nil {
		return out, err
	}
	if s.EnrollmentSource == nil {
		return out, nil
	}
	if err := s.authorizeOwnSummary(ctx, p, school); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return NextClassDay{}, err
		}
		return out, nil
	}
	if err := s.refreshOwn(ctx, p); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return NextClassDay{}, err
		}
		return out, nil
	}
	rows := []OwnSchedule{}
	after := ""
	for pageNumber := 0; pageNumber < 20; pageNumber++ {
		page, err := s.ownSchedulePage(ctx, p, school, "", after, 100, true)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return NextClassDay{}, err
			}
			return out, nil
		}
		rows = append(rows, page.Items...)
		if page.Next == "" {
			break
		}
		if page.Next == after || pageNumber == 19 {
			return NextClassDay{}, ErrUnavailable
		}
		after = page.Next
	}
	now := s.now()
	latestStartedTermKey := 0
	for _, row := range rows {
		if row.Enrollment.TermOrderKey != nil && *row.Enrollment.TermOrderKey > latestStartedTermKey {
			if start, _, ok := timetableBounds(row.Timetable); ok && !start.After(now) {
				latestStartedTermKey = *row.Enrollment.TermOrderKey
			}
		}
	}
	type candidate struct {
		lesson   NextLesson
		zone     string
		teachers []string
	}
	all := []candidate{}
	seen := map[string]bool{}
	hasUnscheduled := false
	for _, row := range rows {
		if row.Enrollment.Subject != p.Subject || row.Enrollment.SchoolID != school {
			return NextClassDay{}, ErrUnavailable
		}
		historical := isHistoricalEndedTerm(row, now, latestStartedTermKey)
		if len(row.Meetings) == 0 {
			if historical {
				continue
			}
			hasUnscheduled = true
			continue
		}
		if row.Timetable == nil {
			if historical {
				continue
			}
			out.Reason = "schedule_incomplete"
			return out, nil
		}
		for _, meeting := range row.Meetings {
			single := row
			single.Meetings = []ScheduleMeeting{meeting}
			occurrences, err := ExpandSchedule(p.Subject, single)
			if err != nil {
				if historical {
					continue
				}
				out.Reason = "schedule_incomplete"
				return out, nil
			}
			for _, occurrence := range occurrences {
				if !occurrence.End.After(now) || seen[occurrence.UID] {
					continue
				}
				seen[occurrence.UID] = true
				all = append(all, candidate{lesson: NextLesson{CourseID: row.Enrollment.CourseID, CourseName: row.CourseName,
					Location: occurrence.Location, StartsAt: occurrence.Start, EndsAt: occurrence.End,
					StartPeriod: meeting.ScheduleTime.StartPeriod, EndPeriod: meeting.ScheduleTime.EndPeriod, Teachers: []Object{}},
					zone: row.Timetable.Timezone, teachers: row.Enrollment.TeacherIDs})
				if len(all) > 10000 {
					return NextClassDay{}, ErrUnavailable
				}
			}
		}
	}
	if len(all) == 0 {
		if hasUnscheduled {
			out.Reason = "schedule_incomplete"
			return out, nil
		}
		out.State, out.Reason = "empty", ""
		return out, nil
	}
	slices.SortFunc(all, func(a, b candidate) int {
		if cmp := a.lesson.StartsAt.Compare(b.lesson.StartsAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.lesson.CourseID, b.lesson.CourseID)
	})
	zone := all[0].zone
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return NextClassDay{}, ErrUnavailable
	}
	date := all[0].lesson.StartsAt.In(loc).Format(time.DateOnly)
	selected := []candidate{}
	teacherIDs := []string{}
	for _, item := range all {
		if item.lesson.StartsAt.In(loc).Format(time.DateOnly) != date {
			break
		}
		if item.zone != zone || len(selected) >= 100 {
			return NextClassDay{}, ErrUnavailable
		}
		selected = append(selected, item)
		teacherIDs = append(teacherIDs, item.teachers...)
	}
	slices.Sort(teacherIDs)
	teacherIDs = slices.Compact(teacherIDs)
	if len(teacherIDs) > 100 {
		return NextClassDay{}, ErrUnavailable
	}
	teachers := map[string]Object{}
	if len(teacherIDs) > 0 {
		refs, err := s.Lookup(teacherMaintenanceContext(ctx), teacherIDs)
		if err != nil {
			return NextClassDay{}, err
		}
		for _, ref := range refs.Items {
			if ref.SchoolID == school && ref.Kind == "teacher" && !ref.Inactive {
				ref.Biography = ""
				teachers[ref.ID] = ref
			}
		}
	}
	for _, item := range selected {
		for _, id := range item.teachers {
			if teacher, ok := teachers[id]; ok {
				item.lesson.Teachers = append(item.lesson.Teachers, teacher)
			}
		}
		out.Items = append(out.Items, item.lesson)
	}
	out.State, out.Reason, out.Date, out.TimeZone = "available", "", &date, zone
	return out, nil
}

type ReviewCourseCandidate struct {
	CourseID   string `json:"course_id"`
	CourseName string `json:"course_name"`
	TermID     string `json:"term_id"`
	TermName   string `json:"term_name"`
}
type OwnReviewCandidates struct {
	Owner, SchoolID string
	Items           []ReviewCourseCandidate
}
type OwnReviewCandidateTransaction interface {
	OwnReviewCandidates(string, string) ([]ReviewCourseCandidate, error)
}

func (s *Service) MyReviewCandidates(ctx context.Context, p Principal) (OwnReviewCandidates, error) {
	out := OwnReviewCandidates{Owner: p.Subject, Items: []ReviewCourseCandidate{}}
	school, err := s.readScope(ctx, p, "")
	if err != nil {
		return out, err
	}
	if s.EnrollmentSource == nil {
		return out, ErrUnavailable
	}
	if err := s.authorizeOwnSummary(ctx, p, school); err != nil {
		return out, err
	}
	if err := s.refreshOwn(ctx, p); err != nil {
		return out, err
	}
	out.SchoolID = school
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(OwnReviewCandidateTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out.Items, err = port.OwnReviewCandidates(p.Subject, school)
		return err
	})
	return out, err
}
