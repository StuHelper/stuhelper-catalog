package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestTimetableFloorInventoryAndCleanupPostgres(t *testing.T) {
	s, p, admin, _ := projectionFixture(t)
	ctx := context.Background()
	repo := &postgres.Repository{DB: admin}
	for _, id := range []string{"pre-spring", "a-old"} {
		_, err := admin.ExecContext(ctx, `INSERT INTO catalog.timetables(school_id,term_id,data)
 VALUES('school',$1,jsonb_build_object('school_id','school','term_id',$1::text,'revision',1))`, id)
		if err != nil {
			t.Fatal(err)
		}
		table := c.Timetable{SchoolID: "school", TermID: id, WeekOneStart: "2023-03-06", Timezone: "Asia/Shanghai",
			Periods: []c.Period{{Number: 1, Start: "08:00", End: "08:45"}}}
		if _, err := s.PutTimetable(ctx, p, table); !errors.Is(err, c.ErrInput) {
			t.Fatal("calendar accepted a timetable before the floor", id, err)
		}
		if _, err := s.GetTimetable(ctx, p, "school", id); !errors.Is(err, c.ErrNotFound) {
			t.Fatal("earlier timetable was returned", id, err)
		}
	}
	floor := c.Timetable{SchoolID: "school", TermID: "z-boundary", WeekOneStart: "2023-09-04", Timezone: "Asia/Shanghai",
		Periods: []c.Period{{Number: 1, Start: "08:00", End: "08:45"}}}
	if _, err := s.PutTimetable(ctx, p, floor); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2023, 9, 3, 16, 0, 0, 0, time.UTC)
	if _, err := s.SchoolTermAt(ctx, "school", now.Add(-time.Second)); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("term resolved before the first Monday", err)
	}
	term, err := s.SchoolTermAt(ctx, "school", now)
	if err != nil || term.ID != floor.TermID {
		t.Fatal("first term failed to resolve", term, err)
	}
	list, err := repo.PruneTimetablesBeforeStart(ctx, "school", nil)
	if err != nil || !slices.Equal(list, []string{"a-old", "pre-spring"}) {
		t.Fatal(list, err)
	}
	wrong := 1
	if _, err := repo.PruneTimetablesBeforeStart(ctx, "school", &wrong); !errors.Is(err, c.ErrConflict) {
		t.Fatal("mismatched inventory was accepted", err)
	}
	expected := 2
	deleted, err := repo.PruneTimetablesBeforeStart(ctx, "school", &expected)
	if err != nil || !slices.Equal(deleted, list) {
		t.Fatal(deleted, err)
	}
	remaining, err := repo.PruneTimetablesBeforeStart(ctx, "school", nil)
	if err != nil || len(remaining) != 0 {
		t.Fatal(remaining, err)
	}
	var calendars, termOrders int
	if err := admin.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM catalog.timetables),
 (SELECT count(*) FROM catalog.term_orders WHERE id IN ('a-old','pre-spring','z-boundary'))`).Scan(&calendars, &termOrders); err != nil || calendars != 1 || termOrders != 3 {
		t.Fatal("cleanup touched records outside its scope", calendars, termOrders, err)
	}
	if got, err := s.GetTimetable(ctx, p, "school", "z-boundary"); err != nil || got.WeekOneStart != floor.WeekOneStart {
		t.Fatal(got, err)
	}
}
