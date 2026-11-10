package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Occurrence is also the time data a notification adapter can consume.
type Occurrence struct {
	UID               string
	Start, End        time.Time
	Summary, Location string
}

// localInstant rejects DST gaps and folds rather than choosing a guessed instant.
func localInstant(date, clock string, loc *time.Location) (time.Time, error) {
	text := date + " " + clock
	v, e := time.ParseInLocation("2006-01-02 15:04", text, loc)
	if e != nil || v.Format("2006-01-02 15:04") != text {
		return time.Time{}, ErrScheduleIncomplete
	}
	for _, d := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		if v.Add(d).Format("2006-01-02 15:04") == text {
			return time.Time{}, ErrScheduleIncomplete
		}
	}
	return v, nil
}
func ExpandSchedule(subject string, row OwnSchedule) ([]Occurrence, error) {
	out := []Occurrence{}
	if len(row.Meetings) == 0 {
		return nil, ErrScheduleIncomplete
	}
	if row.Timetable == nil || !row.Timetable.valid() {
		return nil, ErrScheduleIncomplete
	}
	cfg := row.Timetable
	loc, e := time.LoadLocation(cfg.Timezone)
	if e != nil {
		return nil, ErrScheduleIncomplete
	}
	first, _ := time.Parse("2006-01-02", cfg.WeekOneStart)
	periods := map[int]Period{}
	for _, p := range cfg.Periods {
		periods[p.Number] = p
	}
	for _, m := range row.Meetings {
		v := m.ScheduleTime
		if v == nil || !validSchedule(v) {
			return nil, ErrScheduleIncomplete
		}
		for n := v.StartPeriod; n <= v.EndPeriod; n++ {
			if _, ok := periods[n]; !ok {
				return nil, ErrScheduleIncomplete
			}
		}
		for _, week := range v.Weeks {
			date := first.AddDate(0, 0, (week-1)*7+v.Weekday-1).Format("2006-01-02")
			start, e := localInstant(date, periods[v.StartPeriod].Start, loc)
			if e != nil {
				return nil, e
			}
			end, e := localInstant(date, periods[v.EndPeriod].End, loc)
			if e != nil || !end.After(start) {
				return nil, ErrScheduleIncomplete
			}
			uid := digest([]string{subject, row.Enrollment.SchoolID, row.Enrollment.TermID, row.Enrollment.ClassID, m.ID, fmt.Sprint(week)}) + "@catalog.stuhelper"
			out = append(out, Occurrence{UID: uid, Start: start, End: end, Summary: row.CourseName, Location: m.Location})
		}
	}
	return out, nil
}
func calendarText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\r\n", "\\n", "\r", "\\n", "\n", "\\n", ";", "\\;", ",", "\\,").Replace(s)
}
func calendarLine(b *strings.Builder, s string) {
	// RFC 5545: fold at 75 octets without splitting a UTF-8 character.
	for len(s) > 75 {
		n := 75
		for !utf8.RuneStart(s[n]) {
			n--
		}
		b.WriteString(s[:n])
		b.WriteString("\r\n")
		s = " " + s[n:]
	}
	b.WriteString(s)
	b.WriteString("\r\n")
}
func (s *Service) MyCalendar(ctx context.Context, p Principal, term string) (string, error) {
	if !ValidID(term) {
		return "", ErrInput
	}
	page, e := s.MySchedule(ctx, p, term, "", 100)
	if e != nil {
		return "", e
	}
	if page.Next != "" {
		next, e := s.MySchedule(ctx, p, term, page.Next, 1)
		if e != nil {
			return "", e
		}
		if len(next.Items) > 0 {
			return "", ErrUnavailable
		}
	}
	events := []Occurrence{}
	seen := map[string]bool{}
	for _, row := range page.Items {
		items, e := ExpandSchedule(p.Subject, row)
		if e != nil {
			return "", e
		}
		for _, v := range items {
			if !seen[v.UID] {
				events = append(events, v)
				seen[v.UID] = true
			}
		}
		if len(events) > 10000 {
			return "", ErrUnavailable
		}
	}
	var b strings.Builder
	for _, v := range []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//StuHelper//Catalog//ZH", "CALSCALE:GREGORIAN"} {
		calendarLine(&b, v)
	}
	stamp := s.now().Format("20060102T150405Z")
	for _, v := range events {
		for _, line := range []string{"BEGIN:VEVENT", "UID:" + v.UID, "DTSTAMP:" + stamp, "DTSTART:" + v.Start.UTC().Format("20060102T150405Z"), "DTEND:" + v.End.UTC().Format("20060102T150405Z"), "SUMMARY:" + calendarText(v.Summary), "LOCATION:" + calendarText(v.Location), "END:VEVENT"} {
			calendarLine(&b, line)
		}
	}
	calendarLine(&b, "END:VCALENDAR")
	return b.String(), nil
}
