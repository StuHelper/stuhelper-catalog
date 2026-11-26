package catalog

import (
	"errors"
	"testing"
	"time"
)

func TestSchoolTermCalendarLocalBoundaryAndHolidays(t *testing.T) {
	periods := []SchoolTermPeriod{
		{TermOrder: TermOrder{ID: "spring26", Year: 2026, Season: "spring"}, WeekOneStart: "2026-03-02", Timezone: "Asia/Shanghai"},
		{TermOrder: TermOrder{ID: "summer26", Year: 2026, Season: "summer"}, WeekOneStart: "2026-07-13", Timezone: "Asia/Shanghai"},
		{TermOrder: TermOrder{ID: "autumn26", Year: 2026, Season: "autumn"}, WeekOneStart: "2026-09-07", Timezone: "Asia/Shanghai"},
		{TermOrder: TermOrder{ID: "spring27", Year: 2027, Season: "spring"}, WeekOneStart: "2027-03-01", Timezone: "Asia/Shanghai"},
	}
	for _, row := range []struct{ at, want string }{
		{"2026-07-12T15:59:59Z", "spring26"},
		{"2026-07-12T16:00:00Z", "summer26"},
		{"2026-09-06T15:59:59Z", "summer26"},
		{"2026-09-06T16:00:00Z", "autumn26"},
		{"2027-02-21T16:00:00Z", "autumn26"},
		{"2027-02-28T16:00:00Z", "spring27"},
	} {
		at, err := time.Parse(time.RFC3339, row.at)
		if err != nil {
			t.Fatal(err)
		}
		got, err := schoolTermAt(periods, at)
		if err != nil || got.ID != row.want {
			t.Errorf("%s yielded %q, want %q: %v", row.at, got.ID, row.want, err)
		}
	}
	at := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	// A school without a summer term stays in spring until autumn starts.
	got, err := schoolTermAt([]SchoolTermPeriod{periods[0], periods[2]}, at)
	if err != nil || got.ID != "spring26" {
		t.Fatal("summer break without a summer term", got, err)
	}
	periods[1].WeekOneStart, periods[1].Timezone = "", ""
	if _, err := schoolTermAt(periods, at); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing start between dated terms was treated as reliable", err)
	}
	periods[1].WeekOneStart, periods[1].Timezone = "2026-09-07", "Asia/Shanghai"
	if _, err := schoolTermAt(periods, at); !errors.Is(err, ErrUnavailable) {
		t.Fatal("conflicting start dates were accepted", err)
	}
}
