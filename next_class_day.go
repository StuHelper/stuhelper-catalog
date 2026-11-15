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
	return s.OwnSummaryAccess.AuthorizeOwnSummary(ctx, p, school)
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
	type candidate struct {
		lesson   NextLesson
		zone     string
		teachers []string
	}
	all := []candidate{}
	seen := map[string]bool{}
	after := ""
	now := s.now()
	for pageNumber := 0; pageNumber < 20; pageNumber++ {
		page, err := s.MySchedule(ctx, p, "", after, 100)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return NextClassDay{}, err
			}
			return out, nil
		}
		for _, row := range page.Items {
			if row.Enrollment.Subject != p.Subject || row.Enrollment.SchoolID != school {
				return NextClassDay{}, ErrUnavailable
			}
			if len(row.Meetings) == 0 || row.Timetable == nil {
				out.Reason = "schedule_incomplete"
				return out, nil
			}
			for _, meeting := range row.Meetings {
				single := row
				single.Meetings = []ScheduleMeeting{meeting}
				occurrences, err := ExpandSchedule(p.Subject, single)
				if err != nil {
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
		if page.Next == "" {
			break
		}
		if page.Next == after || pageNumber == 19 {
			return NextClassDay{}, ErrUnavailable
		}
		after = page.Next
	}
	if len(all) == 0 {
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
		refs, err := s.Lookup(ctx, teacherIDs)
		if err != nil {
			return NextClassDay{}, err
		}
		for _, ref := range refs.Items {
			if ref.SchoolID == school && ref.Kind == "teacher" && !ref.Inactive {
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
