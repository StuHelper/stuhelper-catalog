package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

type ScheduleTime struct {
	Weekday     int   `json:"weekday"`
	StartPeriod int   `json:"start_period"`
	EndPeriod   int   `json:"end_period"`
	Weeks       []int `json:"weeks"`
}
type Period struct {
	Number int    `json:"number"`
	Start  string `json:"start"`
	End    string `json:"end"`
}
type Timetable struct {
	SchoolID     string   `json:"school_id"`
	TermID       string   `json:"term_id"`
	Revision     int64    `json:"revision"`
	WeekOneStart string   `json:"week_one_start"`
	Timezone     string   `json:"timezone"`
	Periods      []Period `json:"periods"`
}

var ErrScheduleIncomplete = errors.New("schedule_incomplete")

func validSchedule(v *ScheduleTime) bool {
	if v == nil {
		return true
	}
	if v.Weekday < 1 || v.Weekday > 7 || v.StartPeriod < 1 || v.EndPeriod < v.StartPeriod || v.EndPeriod > 30 || len(v.Weeks) < 1 || len(v.Weeks) > 60 {
		return false
	}
	seen := map[int]bool{}
	for _, w := range v.Weeks {
		if w < 1 || w > 60 || seen[w] {
			return false
		}
		seen[w] = true
	}
	return true
}

// Only this unambiguous grammar is parsed. All original text remains available.
var schedulePattern = regexp.MustCompile(`^周([一二三四五六日天]) 第([0-9]+)-([0-9]+)节 第([0-9]+)-([0-9]+)周(?:\(([单双])\))?$`)

func ParseSchedule(text string) *ScheduleTime {
	m := schedulePattern.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	days := map[string]int{"一": 1, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6, "日": 7, "天": 7}
	v := &ScheduleTime{Weekday: days[m[1]], StartPeriod: n(2), EndPeriod: n(3)}
	if n(4) < 1 || n(5) > 60 || n(4) > n(5) {
		return nil
	}
	for w := n(4); w <= n(5); w++ {
		if m[6] == "单" && w%2 == 0 || m[6] == "双" && w%2 == 1 {
			continue
		}
		v.Weeks = append(v.Weeks, w)
	}
	if !validSchedule(v) {
		return nil
	}
	return v
}
func (v Timetable) valid() bool {
	if !ValidID(v.SchoolID) || !ValidID(v.TermID) || v.Revision < 0 || len(v.Periods) < 1 || len(v.Periods) > 30 {
		return false
	}
	d, e := time.Parse("2006-01-02", v.WeekOneStart)
	if e != nil || d.Weekday() != time.Monday {
		return false
	}
	if v.Timezone == "" || v.Timezone == "Local" {
		return false
	}
	if _, e = time.LoadLocation(v.Timezone); e != nil {
		return false
	}
	lastN, lastEnd := 0, ""
	for _, p := range v.Periods {
		a, e := time.Parse("15:04", p.Start)
		b, f := time.Parse("15:04", p.End)
		if e != nil || f != nil || p.Start != a.Format("15:04") || p.End != b.Format("15:04") || !a.Before(b) || p.Number <= lastN || p.Number > 30 || p.Start < lastEnd {
			return false
		}
		lastN, lastEnd = p.Number, p.End
	}
	return true
}
func (s *Service) GetTimetable(ctx context.Context, p Principal, school, term string) (Timetable, error) {
	var out Timetable
	if !p.CanMaintain || !allowed(p, school) {
		return out, ErrForbidden
	}
	if !ValidID(school) || !ValidID(term) {
		return out, ErrInput
	}
	e := s.Repo.Transaction(ctx, func(t Transaction) error { v, e := t.Timetable(school, term); out = v; return e })
	return out, e
}
func (s *Service) PutTimetable(ctx context.Context, p Principal, in Timetable) (Timetable, error) {
	var out Timetable
	if !p.CanMaintain || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if !in.valid() {
		return out, ErrInput
	}
	e := s.Repo.Transaction(ctx, func(t Transaction) error {
		if e := t.Lock(in.SchoolID); e != nil {
			return e
		}
		term, e := t.Object(in.TermID)
		if e != nil {
			return e
		}
		if term.Object.Kind != "term" || term.Object.SchoolID != in.SchoolID {
			return ErrInput
		}
		old, e := t.Timetable(in.SchoolID, in.TermID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		same := old
		same.Revision = in.Revision
		if old.Revision == in.Revision+1 && digest(same) == digest(in) {
			out = old
			return nil
		}
		if old.Revision != in.Revision {
			return ErrConflict
		}
		out = in
		out.Revision++
		if e = t.SaveTimetable(out); e != nil {
			return e
		}
		return t.Event(Event{ID: digest([]string{"timetable", in.SchoolID, in.TermID, fmt.Sprint(out.Revision)}), Subject: "timetable/" + in.SchoolID + "/" + in.TermID, Type: "catalog.timetable.updated.v1", SchoolID: in.SchoolID, Actor: p.Subject, Revision: out.Revision, Time: s.now()})
	})
	return out, e
}

func normalizeSchedules(objects []Object) []Object {
	out := append([]Object(nil), objects...)
	for i := range out {
		if out[i].Kind == "schedule" && out[i].ScheduleTime == nil {
			out[i].ScheduleTime = ParseSchedule(out[i].TimeText)
		}
	}
	return out
}
