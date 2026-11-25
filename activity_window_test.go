package catalog_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestActivityWindowBeijingCalendar(t *testing.T) {
	for _, tt := range []struct {
		name        string
		at          string
		days        int
		from, until string
		today       string
	}{
		{"before midnight", "2026-01-31T15:59:59.999999999Z", 30, "2026-01-02", "2026-02-01", "2026-01-31"},
		{"midnight", "2026-01-31T16:00:00Z", 1, "2026-02-01", "2026-02-02", "2026-02-01"},
		{"after midnight", "2026-02-01T00:00:00.000000001+08:00", 1, "2026-02-01", "2026-02-02", "2026-02-01"},
		{"leap year", "2024-03-01T16:00:00Z", 366, "2023-03-03", "2024-03-03", "2024-03-02"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			from, until := c.ActivityWindow(at, tt.days)
			wantTime := func(day string) time.Time {
				t.Helper()
				v, err := time.Parse(time.RFC3339, day+"T00:00:00+08:00")
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			if !from.Equal(wantTime(tt.from)) || !until.Equal(wantTime(tt.until)) ||
				from.Format(time.DateOnly) != tt.from || until.Format(time.DateOnly) != tt.until {
				t.Fatal(from, until)
			}
			id := c.ActivityID("course", "course", at)
			_, day, err := c.ParseActivityID(id)
			if err != nil || !day.Equal(wantTime(tt.today)) || !c.ActivityDay(day) || c.ActivityDay(day.Truncate(24*time.Hour)) {
				t.Fatal(id, day, err)
			}
		})
	}
}

func TestDirectoryAllTimeHeatAndMidnightCursor(t *testing.T) {
	ctx := context.Background()
	s, admin, input := fixture(t)
	input.Objects = append(input.Objects, c.Object{ID: "another", Kind: "course", SchoolID: "school", Name: "Another course"})
	if _, err := s.Import(ctx, admin, "heat-all-time", input); err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, 1, 31, 16, 0, 0, 0, time.UTC)
	now := boundary.AddDate(0, 0, -400)
	s.Clock = func() time.Time { return now }
	s.DirectoryMetrics = uiMetrics{}
	principal := c.Principal{Subject: "issuer:student"}
	repo := s.Repo.(*postgres.Repository)
	query := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 1}}
	visit := func() {
		t.Helper()
		if _, err := s.Read(ctx, principal, "course"); err != nil {
			t.Fatal(err)
		}
	}
	readHeat := func(want float64) c.DirectoryPage {
		t.Helper()
		page, err := s.ReadDirectory(ctx, principal, query)
		if err != nil || len(page.Items) != 1 || page.Items[0].Object.ID != "course" || page.Items[0].Heat != want || page.Next == "" {
			t.Fatalf("all-time heat: want %v, got %+v, err %v", want, page, err)
		}
		return page
	}
	visit()
	visit()
	now = boundary.Add(-time.Second)
	first := readHeat(1)
	now = boundary
	continued := query
	continued.After = first.Next
	next, err := s.ReadDirectory(ctx, principal, continued)
	if err != nil || len(next.Items) != 1 || next.Items[0].Object.ID != "another" {
		t.Fatal("continuation after Beijing midnight", next, err)
	}
	if n, err := s.PruneViewKeys(ctx, 1000); err != nil || n != 1 {
		t.Fatal("expired day key", n, err)
	}
	if n, err := repo.PruneViewDedup(ctx, now, 1000); err != nil || n != 1 {
		t.Fatal("expired token", n, err)
	}
	visit()
	visit()
	readHeat(2)
}

func TestDirectoryPageOmitsWindowDays(t *testing.T) {
	raw, err := json.Marshal(c.DirectoryPage{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["window_days"]; exists {
		t.Fatal("all-time directory returned window_days")
	}
}
