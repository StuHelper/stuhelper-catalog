package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"github.com/jackc/pgx/v5/stdlib"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func timetable() c.Timetable {
	return c.Timetable{SchoolID: "school", TermID: "term1", WeekOneStart: "2026-09-07", Timezone: "Asia/Shanghai", Periods: []c.Period{{Number: 1, Start: "08:00", End: "08:45"}, {Number: 2, Start: "08:55", End: "09:40"}}}
}
func TestScheduleExpansionWeeksTimezoneAndMissing(t *testing.T) {
	for _, tc := range []struct {
		text string
		days []string
	}{{"周一 第1-2节 第1-4周(单)", []string{"20260907T000000Z", "20260921T000000Z"}}, {"周一 第1-2节 第1-4周(双)", []string{"20260914T000000Z", "20260928T000000Z"}}, {"周一 第1-2节 第1-2周", []string{"20260907T000000Z", "20260914T000000Z"}}} {
		cfg := timetable()
		row := c.OwnSchedule{Timetable: &cfg, Enrollment: c.Enrollment{TermID: "term1", ClassID: "class1"}, Meetings: []c.ScheduleMeeting{{ID: "m", ScheduleTime: c.ParseSchedule(tc.text)}}}
		events, e := c.ExpandSchedule("owner", row)
		if e != nil || len(events) != len(tc.days) {
			t.Fatal(events, e)
		}
		for i, v := range events {
			if v.Start.UTC().Format("20060102T150405Z") != tc.days[i] || v.End.Sub(v.Start) != 100*time.Minute {
				t.Fatal(v)
			}
		}
		old := events[0].UID
		cfg.Periods[0].Start = "08:10"
		events, e = c.ExpandSchedule("owner", row)
		if e != nil || events[0].UID != old {
			t.Fatal(events, e)
		}
		row.Enrollment.TermID = "term2"
		events, e = c.ExpandSchedule("owner", row)
		if e != nil || events[0].UID == old {
			t.Fatal(events, e)
		}
		row.Timetable = nil
		if _, e = c.ExpandSchedule("owner", row); !errors.Is(e, c.ErrScheduleIncomplete) {
			t.Fatal(e)
		}
		row.Timetable = &cfg
		row.Meetings[0].ScheduleTime = nil
		if _, e = c.ExpandSchedule("owner", row); !errors.Is(e, c.ErrScheduleIncomplete) {
			t.Fatal(e)
		}
	}
	for _, text := range []string{"Monday unknown", "周一 第1-2节 第1-61周", "周一 第3-2节 第1-4周", "周一 第1-2节 第0-4周"} {
		if c.ParseSchedule(text) != nil {
			t.Fatal(text)
		}
	}
	// Spring gap and autumn fold are explicit failures; normal DST dates convert correctly.
	for _, date := range []string{"2026-03-02", "2026-10-26"} {
		cfg := timetable()
		cfg.Timezone = "America/New_York"
		cfg.WeekOneStart = date
		cfg.Periods = []c.Period{{Number: 1, Start: "01:30", End: "02:30"}}
		row := c.OwnSchedule{Timetable: &cfg, Meetings: []c.ScheduleMeeting{{ScheduleTime: &c.ScheduleTime{Weekday: 7, StartPeriod: 1, EndPeriod: 1, Weeks: []int{1}}}}}
		if _, e := c.ExpandSchedule("owner", row); !errors.Is(e, c.ErrScheduleIncomplete) {
			t.Fatal(date, e)
		}
	}
}
func TestStructuredImportPreviewCalendarAndOwnership(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	owner := c.Principal{Subject: "issuer:student"}
	in.Objects = append(in.Objects, c.Object{ID: "meeting", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Meeting", TimeText: "周一 第1-2节 第1-4周(单)", Location: "教室,一;二\n三"}, c.Object{ID: "unknown", Kind: "schedule", SchoolID: "school", ClassID: "class2", Name: "Unknown", TimeText: "学校尚未确认"})
	preview, e := s.PreviewImport(ctx, admin, in)
	if e != nil || !preview.Valid {
		t.Fatal(preview, e)
	}
	found := false
	for _, v := range preview.Changes {
		if v.ID == "meeting" {
			found = v.ScheduleTime != nil && len(v.ScheduleTime.Weeks) == 2 && v.TimeText == in.Objects[len(in.Objects)-2].TimeText
		}
	}
	if !found {
		t.Fatal(preview)
	}
	if _, e = s.Get(ctx, "meeting"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("preview wrote data", e)
	}
	in.PreviewToken = preview.PreviewToken
	if _, e = s.Import(ctx, admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	unknown, e := s.Get(ctx, "unknown")
	if e != nil || unknown.TimeText != "学校尚未确认" || unknown.ScheduleTime != nil {
		t.Fatal(unknown, e)
	}
	if _, e = s.MyCalendar(ctx, owner, "term1"); !errors.Is(e, c.ErrScheduleIncomplete) {
		t.Fatal(e)
	}
	cfg, e := s.PutTimetable(ctx, admin, timetable())
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.MySchedule(ctx, owner, "term1", "", 20)
	if e != nil || len(page.Items) != 1 || page.Items[0].Timetable.Revision != cfg.Revision || page.Items[0].Meetings[0].ScheduleTime.Weekday != 1 || len(page.Items[0].Enrollment.TeacherIDs) != 2 {
		t.Fatal(page, e)
	}
	calendar, e := s.MyCalendar(ctx, owner, "term1")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"BEGIN:VCALENDAR\r\n", "DTSTART:20260907T000000Z", "DTSTART:20260921T000000Z", "DTEND:20260907T014000Z", "LOCATION:教室\\,一\\;二\\n三", "END:VCALENDAR\r\n"} {
		if !strings.Contains(calendar, v) {
			t.Fatal(v, calendar)
		}
	}
	if strings.Count(calendar, "BEGIN:VEVENT") != 2 {
		t.Fatal(calendar)
	}
	other, e := s.MyCalendar(ctx, c.Principal{Subject: "other"}, "term1")
	if e != nil || strings.Contains(other, "VEVENT") {
		t.Fatal(other, e)
	}
	other, e = s.MyCalendar(ctx, owner, "term2")
	if e != nil || strings.Contains(other, "VEVENT") {
		t.Fatal(other, e)
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return owner, nil }}.Routes()
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/v1/catalog/me/schedule?term_id=term1", 200}, {"/api/v1/catalog/me/schedule/calendar?term_id=term1", 200}, {"/api/v1/catalog/me/schedule?term_id=term1&subject=other", 400}, {"/api/v1/catalog/admin/schools/school/terms/term1/timetable", 403}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}

	// Removing parseable time must be visible as a structured-field removal in preview.
	changed := in
	changed.PreviewToken = ""
	changed.Revision = 2
	changed.Objects = append([]c.Object(nil), in.Objects...)
	changed.Objects[len(changed.Objects)-2].TimeText = "学校尚未确认"
	removal, e := s.PreviewImport(ctx, admin, changed)
	if e != nil || !removal.Valid {
		t.Fatal(removal, e)
	}
	found = false
	for _, v := range removal.Changes {
		if v.ID == "meeting" {
			for _, field := range v.Fields {
				if field == "schedule_time" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("preview hid removed structured time", removal)
	}
	// Explicit structured values are validated during preview as well as import.
	in.PreviewToken = ""
	in.Revision++
	in.Objects[len(in.Objects)-2].ScheduleTime = &c.ScheduleTime{Weekday: 1, StartPeriod: 1, EndPeriod: 2, Weeks: []int{1, 1}}
	invalid, e := s.PreviewImport(ctx, admin, in)
	if e != nil || invalid.Valid || invalid.Code != "invalid_input" {
		t.Fatal(invalid, e)
	}
}
func TestTimetableRevisionAuditConcurrencyAndMigration(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	if _, e := s.Import(ctx, admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	repo := s.Repo.(*postgres.Repository)
	if _, e := s.GetTimetable(ctx, admin, "school", "term1"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := s.PutTimetable(ctx, c.Principal{Subject: "other", CanMaintain: true, Schools: []string{"other"}}, timetable()); !errors.Is(e, c.ErrForbidden) {
		t.Fatal(e)
	}
	cfg, e := s.PutTimetable(ctx, admin, timetable())
	if e != nil || cfg.Revision != 1 {
		t.Fatal(cfg, e)
	}
	replay, e := s.PutTimetable(ctx, admin, timetable())
	if e != nil || replay.Revision != 1 {
		t.Fatal(replay, e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, start := range []string{"08:05", "08:10"} {
		wg.Add(1)
		go func(start string) {
			defer wg.Done()
			v := timetable()
			v.Revision = 1
			v.Periods[0].Start = start
			_, e := s.PutTimetable(ctx, admin, v)
			results <- e
		}(start)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, c.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	var audits, events int
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.audit WHERE data->>'Type'='catalog.timetable.updated.v1'`).Scan(&audits); e != nil {
		t.Fatal(e)
	}
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.timetable.updated.v1' AND sent_at IS NULL`).Scan(&events); e != nil || audits != 2 || events != 2 {
		t.Fatal(audits, events, e)
	}
	v, e := s.GetTimetable(ctx, admin, "school", "term1")
	if e != nil || v.Revision != 2 {
		t.Fatal(v, e)
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return admin, nil }}.Routes()
	b, _ := json.Marshal(v)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PUT", "/api/v1/catalog/admin/schools/school/terms/term1/timetable", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTimetableTransactionNotificationAndMigration(t *testing.T) {
	repo, migration := database(t)
	ctx := context.Background()
	s := &c.Service{Repo: repo}
	admin := c.Principal{Subject: "issuer:admin", Schools: []string{"school"}, CanImport: true, CanMaintain: true}
	in := c.Import{SchoolID: "school", SourceID: "source", Revision: 1, FetchedAt: time.Now(), Objects: []c.Object{{ID: "school", Kind: "school", SchoolID: "school", Name: "School"}, {ID: "term1", Kind: "term", SchoolID: "school", Name: "Term"}}}
	if _, e := s.Import(ctx, admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	listener, e := repo.DB.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if _, e = listener.ExecContext(ctx, `LISTEN catalog_outbox`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PutTimetable(ctx, admin, timetable()); e != nil {
		t.Fatal(e)
	}
	timeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if e = listener.Raw(func(raw any) error {
		n, e := raw.(*stdlib.Conn).Conn().WaitForNotification(timeout)
		if e == nil && n.Channel != "catalog_outbox" {
			t.Error(n)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	// A failed audit must roll back the configuration and its pending event.
	if _, e = migration.Exec(`CREATE FUNCTION catalog.reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$; CREATE TRIGGER reject_audit BEFORE INSERT ON catalog.audit FOR EACH ROW EXECUTE FUNCTION catalog.reject_audit()`); e != nil {
		t.Fatal(e)
	}
	cfg := timetable()
	cfg.Revision = 1
	cfg.Periods[0].Start = "08:10"
	if _, e = s.PutTimetable(ctx, admin, cfg); e == nil {
		t.Fatal("audit failure accepted")
	}
	stored, e := s.GetTimetable(ctx, admin, "school", "term1")
	if e != nil || stored.Revision != 1 || stored.Periods[0].Start != "08:00" {
		t.Fatal(stored, e)
	}
	var count int
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.timetable.updated.v1'`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	// Reapplying schema preserves pending events, configuration, and confirmations.
	if _, e = migration.Exec(`UPDATE catalog.outbox SET sent_at=now() WHERE data->>'type'='catalog.imported.v1'`); e != nil {
		t.Fatal(e)
	}
	if _, e = migration.Exec(postgres.Schema); e != nil {
		t.Fatal(e)
	}
	if e = repo.DB.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE sent_at IS NOT NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	stored, e = s.GetTimetable(ctx, admin, "school", "term1")
	if e != nil || stored.Revision != 1 {
		t.Fatal(stored, e)
	}
}

func TestCalendarEscapingFoldingAndDSTOffsets(t *testing.T) {
	cfg := timetable()
	cfg.Timezone = "America/New_York"
	cfg.WeekOneStart = "2026-03-02"
	row := c.OwnSchedule{Timetable: &cfg, Meetings: []c.ScheduleMeeting{{ID: "m", ScheduleTime: &c.ScheduleTime{Weekday: 1, StartPeriod: 1, EndPeriod: 2, Weeks: []int{1, 2}}}}}
	events, e := c.ExpandSchedule("owner", row)
	if e != nil || events[0].Start.UTC().Hour() != 13 || events[1].Start.UTC().Hour() != 12 {
		t.Fatal(events, e)
	}
	s, admin, in := fixture(t)
	ctx := context.Background()
	name := strings.Repeat("课程名称", 30) + ",;\\\nEND:VEVENT"
	for i := range in.Objects {
		if in.Objects[i].Kind == "course" {
			in.Objects[i].Name = name
		}
	}
	in.Objects = append(in.Objects, c.Object{ID: "meeting", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Meeting", ScheduleTime: c.ParseSchedule("周一 第1-2节 第1-2周")})
	if _, e = s.Import(ctx, admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PutTimetable(ctx, admin, timetable()); e != nil {
		t.Fatal(e)
	}
	calendar, e := s.MyCalendar(ctx, c.Principal{Subject: "issuer:student"}, "term1")
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range strings.Split(calendar, "\r\n") {
		if len(line) > 75 || !utf8.ValidString(line) {
			t.Fatal(line)
		}
	}
	unfolded := strings.ReplaceAll(calendar, "\r\n ", "")
	if strings.Count(unfolded, "\r\nEND:VEVENT\r\n") != 2 || !strings.Contains(unfolded, "\\,\\;\\\\\\nEND:VEVENT") {
		t.Fatal(unfolded)
	}
}
