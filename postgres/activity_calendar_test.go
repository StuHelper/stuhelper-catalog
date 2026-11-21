package postgres

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestActivityCalendarMigrationRebinsViewsAndPreservesWritesAcrossRollback(t *testing.T) {
	repo, owner := activityDatabaseWithSchema(t, baseSchema+"\n"+courseSearchSchema)
	ctx := t.Context()
	beforeBeijingMidnight := time.Date(2024, 10, 31, 15, 59, 59, 999999999, time.UTC)
	beijingMidnight := beforeBeijingMidnight.Add(time.Nanosecond)
	oldDate, newDate := c.ActivityDate(beforeBeijingMidnight), c.ActivityDate(beijingMidnight)
	target := "exact-" + strings.Repeat("x", 80)
	record := c.Object{Kind: "course", ID: target, SchoolID: "school", Name: "Legacy course", Revision: 1, UpdatedAt: beforeBeijingMidnight}
	discoveryID := "catalog-discovery-fixture"
	if err := repo.Transaction(ctx, func(tx c.Transaction) error {
		if err := tx.SaveObject(c.Record{Object: record}); err != nil {
			return err
		}
		return tx.(*transaction).saveEnvelope(c.Envelope{
			SpecVersion: "1.0", ID: discoveryID, Source: "catalog", Type: "catalog.discovery.v1",
			Subject: "course", Time: beforeBeijingMidnight, Data: c.ResultData{Revision: 1},
		})
	}); err != nil {
		t.Fatal(err)
	}

	// The same old UTC day has two exact late views, one exact early view,
	// and an unrelated object for which only the daily total survives.
	firstEvent := writeLegacyViewSnapshot(t, repo.DB, target, beforeBeijingMidnight, 1, true)
	writeLegacyViewSnapshot(t, repo.DB, target, beijingMidnight, 1, true)
	writeLegacyViewSnapshot(t, repo.DB, target, beijingMidnight.Add(time.Hour), 1, true)
	writeLegacyViewSnapshot(t, repo.DB, "aggregate-only", beforeBeijingMidnight, 5, false)
	writeLegacyViewSnapshot(t, repo.DB, "late-only", beijingMidnight, 1, true)
	writeLegacyViewSnapshot(t, repo.DB, "late-with-pruned-history", beforeBeijingMidnight, 1, false)
	writeLegacyViewSnapshot(t, repo.DB, "late-with-pruned-history", beforeBeijingMidnight, 1, false)
	writeLegacyViewSnapshot(t, repo.DB, "late-with-pruned-history", beijingMidnight, 1, true)
	if err := repo.ConfirmOutbox(ctx, firstEvent); err != nil {
		t.Fatal(err)
	}
	// The preceding outbox revision can still prove the second view even
	// when its prior history has been swept.
	if _, err := owner.ExecContext(ctx, `DELETE FROM catalog.activity_history WHERE id=$1 AND revision=1`, c.ActivityID("course", target, oldDate)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `DELETE FROM catalog.activity_history WHERE id=$1 AND revision<3`, c.ActivityID("course", "late-with-pruned-history", oldDate)); err != nil {
		t.Fatal(err)
	}
	if err := RollbackActivityCalendar(ctx, owner); err != nil {
		t.Fatal("rollback before first migration:", err)
	}
	_, cursor, err := repo.ActivitySnapshot(ctx, oldDate, newDate.AddDate(0, 0, 1), "", 1)
	if err != nil || cursor == "" {
		t.Fatalf("legacy pagination cursor missing: %v", err)
	}
	if err := Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}

	if _, _, err := repo.ActivitySnapshot(ctx, oldDate, newDate.AddDate(0, 0, 1), cursor, 1); !errors.Is(err, c.ErrUnavailable) {
		t.Fatalf("legacy cursor still usable: %v", err)
	}
	check := func(target string, day time.Time, views int64) c.Activity {
		t.Helper()
		id := c.ActivityID("course", target, day)
		row, err := repo.ReadActivity(ctx, id)
		if err != nil || row.Metrics["views"] != views || row.Day != day.Format(time.DateOnly) || row.Visible != (views > 0) {
			t.Fatalf("activity %s: got %+v, %v, want %d", id, row, err, views)
		}
		return row
	}
	initial := check(target, oldDate, 1)
	check(target, newDate, 2)
	check("aggregate-only", oldDate, 3)
	check("aggregate-only", newDate, 2)
	check("late-only", oldDate, 0)
	check("late-only", newDate, 1)
	check("late-with-pruned-history", oldDate, 1)
	check("late-with-pruned-history", newDate, 2)
	assertActivityCalendarQueue(t, owner, discoveryID, firstEvent, c.ActivityEventType, "discovery.activity.v1")

	if err := Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if got := check(target, oldDate, 1); got.Revision != initial.Revision {
		t.Fatalf("migration repeated a revision: %d", got.Revision)
	}

	// The generic runtime grants must not expose the ledger or allow changing
	// its mode. The normal runtime write still drives the owner trigger.
	var runtimeRole string
	if err := repo.DB.QueryRowContext(ctx, `SELECT current_user`).Scan(&runtimeRole); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `GRANT SELECT,UPDATE ON catalog.activity_calendar, catalog.activity_view_segments TO `+runtimeRole); err != nil {
		t.Fatal(err)
	}
	var visibleSegments int
	if err := repo.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog.activity_view_segments`).Scan(&visibleSegments); err != nil || visibleSegments != 0 {
		t.Fatalf("runtime ledger rows: %d, %v", visibleSegments, err)
	}
	result, err := repo.DB.ExecContext(ctx, `UPDATE catalog.activity_calendar SET beijing=false`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 0 {
		t.Fatalf("runtime changed calendar rows: %d, %v", n, err)
	}
	if err := repo.Transaction(ctx, func(tx c.Transaction) error {
		created, err := tx.RecordView("new-reader", record, beijingMidnight.Add(2*time.Hour))
		if err == nil && !created {
			t.Error("post-cutover view was not recorded")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transaction(ctx, func(tx c.Transaction) error {
		created, err := tx.RecordView("new-reader-before-midnight", record, beforeBeijingMidnight)
		if err == nil && !created {
			t.Error("post-cutover view just before midnight was not recorded")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(target, oldDate, 2)
	check(target, newDate, 3)

	if err := RollbackActivityCalendar(ctx, owner); err != nil {
		t.Fatal(err)
	}
	rollback := check(target, oldDate, 5)
	check(target, newDate, 0)
	check("aggregate-only", oldDate, 5)
	check("aggregate-only", newDate, 0)
	assertActivityCalendarQueue(t, owner, discoveryID, firstEvent, "discovery.activity.v1", c.ActivityEventType)
	if err := RollbackActivityCalendar(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if got := check(target, oldDate, 5); got.Revision != rollback.Revision {
		t.Fatalf("rollback repeated a revision: %d", got.Revision)
	}
	writeLegacyViewSnapshot(t, repo.DB, target, beijingMidnight.AddDate(0, 0, 1), 1, true)
	if err := Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	check(target, oldDate, 2)
	check(target, newDate, 3)
	check(target, newDate.AddDate(0, 0, 1), 1)
	assertActivityCalendarQueue(t, owner, discoveryID, firstEvent, c.ActivityEventType, "discovery.activity.v1")
}

func TestActivityCalendarPartialUTCDateUsesElapsedTime(t *testing.T) {
	repo, owner := activityDatabase(t)
	ctx := t.Context()
	utcMidnight := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	writeLegacyViewSnapshot(t, repo.DB, "partial-summary", utcMidnight, 15, false)
	for _, tt := range []struct {
		name       string
		elapsed    time.Duration
		earlyViews int
		lateViews  int
	}{
		{"before Beijing midnight", 16*time.Hour - time.Minute, 15, 0},
		{"at Beijing midnight", 16 * time.Hour, 15, 0},
		{"after Beijing midnight", 20 * time.Hour, 12, 3},
		{"past UTC date", 24 * time.Hour, 10, 5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `SELECT catalog.seed_view_segments($1)`, utcMidnight.Add(tt.elapsed)); err != nil {
				t.Fatal(err)
			}
			var early, late int
			err = tx.QueryRowContext(ctx, `SELECT early_views, late_views FROM catalog.activity_view_segments WHERE target_id='partial-summary'`).Scan(&early, &late)
			if err != nil || early != tt.earlyViews || late != tt.lateViews {
				t.Fatalf("view split = %d / %d, want %d / %d: %v", early, late, tt.earlyViews, tt.lateViews, err)
			}
		})
	}
}

// Model the preceding executable's atomic full-snapshot write. An event may
// have expired from outbox even though its latest daily total still exists.
func writeLegacyViewSnapshot(t *testing.T, db *sql.DB, target string, at time.Time, delta int64, keepEvent bool) string {
	t.Helper()
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	day := at.UTC().Truncate(24 * time.Hour)
	date := day.Format(time.DateOnly)
	id := c.ActivityID("course", target, day)
	var views, revision int64
	err = tx.QueryRowContext(ctx, `INSERT INTO catalog.activity_buckets AS b(id,kind,target_id,school_id,day,first_count,second_count,revision,visible)
VALUES($1,'course',$2,'school',$3,$4,0,1,true)
ON CONFLICT(id) DO UPDATE SET first_count=b.first_count+EXCLUDED.first_count,revision=b.revision+1,visible=true
RETURNING first_count,revision`, id, target, date, delta).Scan(&views, &revision)
	if err != nil {
		t.Fatal(err)
	}
	activity := c.Activity{
		Source: "catalog", ID: id, Target: c.ActivityTarget{Source: "catalog", Kind: "course", ID: target},
		Day: date, Revision: revision, Visible: true, Metrics: map[string]int64{"views": views},
	}
	data, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog.activity_buckets SET data=$2 WHERE id=$1`, id, data); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog.activity_history(id,revision,day,data) VALUES ($1,$2,$3,$4)`, id, revision, date, data); err != nil {
		t.Fatal(err)
	}
	eventID := ""
	if keepEvent {
		eventID = fmt.Sprintf("activity:%x:%d", sha256.Sum256([]byte(id)), revision)
		targetDTO := c.DiscoveryTarget{Source: "catalog", Kind: "course", ID: target}
		err := (&transaction{tx: tx, ctx: ctx}).saveEnvelope(c.Envelope{
			SpecVersion: "1.0", ID: eventID, Source: "catalog", Type: "discovery.activity.v1", Subject: id, Time: at,
			Data: c.ResultData{Source: "catalog", ID: id, Target: &targetDTO, Day: date, Revision: revision, Visible: &activity.Visible, Metrics: activity.Metrics},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func assertActivityCalendarQueue(t *testing.T, db *sql.DB, discoveryID, confirmedID, activeType, obsoleteType string) {
	t.Helper()
	var current, buckets, obsolete, discovery, confirmed int
	err := db.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE e.data->>'type'=$1 AND sent_at IS NULL AND e.data->'data'=b.data),
(SELECT count(*) FROM catalog.activity_buckets),
count(*) FILTER(WHERE e.data->>'type'=$2 AND sent_at IS NULL),
count(*) FILTER(WHERE e.id=$3 AND sent_at IS NULL),
count(*) FILTER(WHERE e.id=$4 AND sent_at IS NOT NULL)
FROM catalog.outbox e LEFT JOIN catalog.activity_buckets b ON b.id=e.data->>'subject'`,
		activeType, obsoleteType, discoveryID, confirmedID).Scan(&current, &buckets, &obsolete, &discovery, &confirmed)
	if err != nil || current != buckets || obsolete != 0 || discovery != 1 || confirmed != 1 {
		t.Fatalf("activity outbox current=%d buckets=%d obsolete=%d discovery=%d confirmed=%d: %v", current, buckets, obsolete, discovery, confirmed, err)
	}
}
