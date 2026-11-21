package catalog_test

import (
	"context"
	"errors"
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

type observedDirectoryWindow struct{ from, until time.Time }

func (m *observedDirectoryWindow) Metrics(_ context.Context, _ c.Principal, _, _ string, from, until time.Time) ([]c.DirectoryMetric, error) {
	m.from, m.until = from, until
	return nil, nil
}

func TestDirectoryHeatAndViewsRollOverAtBeijingMidnight(t *testing.T) {
	ctx := context.Background()
	s, admin, input := fixture(t)
	input.Objects = append(input.Objects, c.Object{ID: "another", Kind: "course", SchoolID: "school", Name: "Another course"})
	if _, err := s.Import(ctx, admin, "heat-window", input); err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, 1, 31, 16, 0, 0, 0, time.UTC)
	now := boundary.Add(-time.Second)
	s.Clock = func() time.Time { return now }
	metrics := &observedDirectoryWindow{}
	s.DirectoryMetrics = metrics
	s.DirectoryConfig = c.DefaultDirectoryConfig()
	s.DirectoryConfig.WindowDays = 1
	principal := c.Principal{Subject: "issuer:student"}
	repo := s.Repo.(*postgres.Repository)
	query := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 1}}
	visit := func() {
		t.Helper()
		if _, err := s.Read(ctx, principal, "course"); err != nil {
			t.Fatal(err)
		}
	}
	readHeat := func(wantFrom, wantUntil time.Time, heat float64) c.DirectoryPage {
		t.Helper()
		page, err := s.ReadDirectory(ctx, principal, query)
		if err != nil || page.WindowDays != 1 || len(page.Items) != 1 || page.Items[0].Heat != heat || page.Next == "" {
			t.Fatal(page, err)
		}
		if !metrics.from.Equal(wantFrom) || !metrics.until.Equal(wantUntil) {
			t.Fatal(metrics.from, metrics.until)
		}
		return page
	}
	visit()
	visit()
	old := readHeat(boundary.AddDate(0, 0, -1), boundary, 1)
	now = boundary
	continued := query
	continued.After = old.Next
	if _, err := s.ReadDirectory(ctx, principal, continued); !errors.Is(err, c.ErrConflict) {
		t.Fatal("cursor from previous Beijing day", err)
	}
	// This reuses the previous directory membership while loading a new day of views.
	readHeat(boundary, boundary.AddDate(0, 0, 1), 0)
	visit()
	readHeat(boundary, boundary.AddDate(0, 0, 1), 1)

	if n, err := s.PruneViewKeys(ctx, 1000); err != nil || n != 1 {
		t.Fatal("previous Beijing day's key", n, err)
	}
	if n, err := repo.PruneViewDedup(ctx, now, 1000); err != nil || n != 1 {
		t.Fatal("previous Beijing day's token", n, err)
	}
	visit()
	docs, _, err := repo.ActivitySnapshot(ctx, boundary.AddDate(0, 0, -1), boundary.AddDate(0, 0, 1), "", 10)
	if err != nil || len(docs) != 2 || docs[0].Day != "2026-01-31" || docs[0].Metrics["views"] != 1 ||
		docs[1].Day != "2026-02-01" || docs[1].Metrics["views"] != 1 {
		t.Fatal(docs, err)
	}
}
