package catalog_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

type pruningEnrollmentSource struct {
	calls atomic.Int64
}

func (s *pruningEnrollmentSource) OwnSnapshot(context.Context, string) (c.EnrollmentSnapshot, error) {
	s.calls.Add(1)
	return c.EnrollmentSnapshot{}, errors.New("should not be called on invalid input")
}

func TestBoundedPruningAndInputValidation(t *testing.T) {
	t.Run("my_enrollments_validates_filter_ids_before_external_refresh", func(t *testing.T) {
		s, admin, in := fixture(t)
		ctx := context.Background()
		if _, err := s.Import(ctx, admin, "seed-enroll-val", in); err != nil {
			t.Fatal(err)
		}
		source := &pruningEnrollmentSource{}
		s.EnrollmentSource = source
		student := c.Principal{Subject: "issuer:student"}

		for _, tc := range []struct {
			name, school, course, term, after string
		}{
			{"invalid_school", "bad school!", "", "", ""},
			{"invalid_course", "school", "../course", "", ""},
			{"invalid_term", "school", "course", "bad\x00term", ""},
			{"invalid_after", "school", "course", "term1", "bad cursor!"},
		} {
			if _, err := s.MyEnrollments(ctx, student, tc.school, tc.course, tc.term, tc.after, 20); !errors.Is(err, c.ErrInput) {
				t.Errorf("%s: got %v, want ErrInput", tc.name, err)
			}
		}
		if got := source.calls.Load(); got != 0 {
			t.Fatalf("MyEnrollments called EnrollmentSource %d times on invalid IDs, want 0", got)
		}
	})

	t.Run("teacher_sites_url_bounds_normalization_and_school_hosts", func(t *testing.T) {
		s, p, snapshot := teacherDirectoryFixture(t)
		ctx := context.Background()

		for _, badURL := range []string{
			"https://shi.buaa.edu.cn/teacher\x00/index.htm",
			"https://shi.buaa.edu.cn/teacher%00/index.htm",
			"https://shi.buaa.edu.cn/" + strings.Repeat("a", 520) + "/index.htm",
			"https://shi.buaa.edu.cn/a/../b/index.htm",
			"https://shi.buaa.edu.cn//synthetic2/index.htm",
			"https://shi.buaa.edu.cn/index.htm",
		} {
			bad := snapshot
			bad.Revision++
			bad.Matches = []c.TeacherSiteMatch{{TeacherID: "teacher2", Name: "teacher2", DepartmentID: "dept", URL: badURL}}
			if _, err := s.ImportTeacherSites(ctx, p, bad); !errors.Is(err, c.ErrInput) {
				t.Errorf("ImportTeacherSites(%q) got %v, want ErrInput", badURL, err)
			}
		}

		// Per-school TeacherSiteHosts configuration enforces configured hostnames for that school.
		s.TeacherSiteHosts = map[string][]string{"school": {"faculty.example.edu.cn"}}
		defaultHost := snapshot
		defaultHost.Revision = 10
		defaultHost.Matches = []c.TeacherSiteMatch{{TeacherID: "teacher2", Name: "teacher2", DepartmentID: "dept", URL: "https://shi.buaa.edu.cn/synthetic2/zh_CN/index.htm"}}
		if _, err := s.ImportTeacherSites(ctx, p, defaultHost); !errors.Is(err, c.ErrInput) {
			t.Fatalf("expected non-configured host rejected, got %v", err)
		}
		customHost := snapshot
		customHost.Revision = 11
		customHost.Matches = []c.TeacherSiteMatch{{TeacherID: "teacher2", Name: "teacher2", DepartmentID: "dept", URL: "https://faculty.example.edu.cn/synthetic2/zh_CN/index.htm"}}
		if res, err := s.ImportTeacherSites(ctx, p, customHost); err != nil || res.Official != 1 {
			t.Fatalf("expected configured school host accepted: res=%+v err=%v", res, err)
		}
	})

	t.Run("bounded_pruning_outbox_replays_and_teacher_refresh_runs", func(t *testing.T) {
		s, admin, in := fixture(t)
		ctx := context.Background()
		baseTime := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
		now := baseTime
		s.Clock = func() time.Time { return now }
		in.FetchedAt = now
		if _, err := s.Import(ctx, admin, "seed-prune", in); err != nil {
			t.Fatal(err)
		}
		repo := s.Repo.(*postgres.Repository)

		// 1. Outbox: confirm 2 events, leave rest unsent; PruneSentOutbox with limit=1 deletes 1 confirmed row and preserves unsent rows.
		pending, err := repo.PendingOutbox(ctx, 10, nil)
		if err != nil || len(pending) < 3 {
			t.Fatalf("expected >=3 pending outbox events, got %d err=%v", len(pending), err)
		}
		for _, ev := range pending[:2] {
			if err := repo.ConfirmOutbox(ctx, ev.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.PruneSentOutbox(ctx, time.Time{}, 10); !errors.Is(err, c.ErrInput) {
			t.Fatalf("expected ErrInput on zero cutoff, got %v", err)
		}
		if _, err := repo.PruneSentOutbox(ctx, time.Now().Add(time.Hour), 1001); !errors.Is(err, c.ErrInput) {
			t.Fatalf("expected ErrInput on limit > 1000, got %v", err)
		}
		deletedOutbox, err := repo.PruneSentOutbox(ctx, time.Now().Add(time.Hour), 1)
		if err != nil || deletedOutbox != 1 {
			t.Fatalf("PruneSentOutbox limit=1 deleted %d err=%v, want 1", deletedOutbox, err)
		}
		stillPending, err := repo.PendingOutbox(ctx, 100, nil)
		if err != nil || len(stillPending) != len(pending)-2 {
			t.Fatalf("PruneSentOutbox touched unsent rows: got %d want %d err=%v", len(stillPending), len(pending)-2, err)
		}

		// 2. Replays: Correct creates an expiring 24h maintenance replay, while Import replay is permanent.
		title := "Corrected Title"
		if _, err := s.Correct(ctx, admin, "course", "temp-correct-1", c.Correction{Revision: 1, Name: &title}); err != nil {
			t.Fatal(err)
		}
		title2 := "Corrected Title 2"
		if _, err := s.Correct(ctx, admin, "course", "temp-correct-2", c.Correction{Revision: 2, Name: &title2}); err != nil {
			t.Fatal(err)
		}
		cutoff := now.Add(48 * time.Hour)
		deletedReplays, err := repo.PruneReplays(ctx, cutoff, 1)
		if err != nil || deletedReplays != 1 {
			t.Fatalf("PruneReplays limit=1 deleted %d err=%v, want 1", deletedReplays, err)
		}
		deletedReplays2, err := repo.PruneReplays(ctx, cutoff, 10)
		if err != nil || deletedReplays2 != 1 {
			t.Fatalf("PruneReplays remainder deleted %d err=%v, want 1", deletedReplays2, err)
		}
		// Permanent Import replay ("seed-prune") must survive PruneReplays!
		now = cutoff.Add(time.Hour)
		if _, err := s.Import(ctx, admin, "seed-prune", in); err != nil {
			t.Fatalf("permanent Import replay was pruned: %v", err)
		}

		// 3. TeacherRefreshRuns: prune terminal runs older than cutoff while preserving active/latest per school.
		refresher := admin
		refresher.CanRefreshTeacherSites = true
		refresher.CanReadTeacherSites = true
		for i, key := range []string{"run-1", "run-2", "run-3"} {
			now = baseTime.Add(time.Duration(i) * time.Hour)
			run, err := s.QueueTeacherRefresh(ctx, refresher, "school", "incremental", "manual", key)
			if err != nil {
				t.Fatal(err)
			}
			now = run.DeadlineAt.Add(time.Minute)
			if _, err := s.TeacherRefreshStatus(ctx, refresher, "school"); err != nil {
				t.Fatal(err)
			}
		}
		// Now 3 timed_out runs exist for "school"; run-3 is the latest and must be preserved!
		deletedRuns, err := repo.PruneTeacherRefreshRuns(ctx, baseTime.Add(10*time.Hour), 1)
		if err != nil || deletedRuns != 1 {
			t.Fatalf("PruneTeacherRefreshRuns limit=1 deleted %d err=%v, want 1", deletedRuns, err)
		}
		deletedRunsRest, err := repo.PruneTeacherRefreshRuns(ctx, baseTime.Add(10*time.Hour), 10)
		if err != nil || deletedRunsRest != 1 {
			t.Fatalf("PruneTeacherRefreshRuns remainder deleted %d err=%v, want 1 (latest preserved)", deletedRunsRest, err)
		}
		replay, err := s.QueueTeacherRefresh(ctx, refresher, "school", "incremental", "manual", "run-1")
		if err != nil || replay.Status != "timed_out" || !replay.RequestedAt.Equal(baseTime) {
			t.Fatal("pruned refresh should replay its original terminal result", replay, err)
		}
		if _, err := s.QueueTeacherRefresh(ctx, refresher, "school", "full", "manual", "run-1"); !errors.Is(err, c.ErrConflict) {
			t.Fatal("pruned refresh allowed its request parameters to change", err)
		}
		status, err := s.TeacherRefreshStatus(ctx, refresher, "school")
		if err != nil || status.Latest == nil {
			t.Fatalf("PruneTeacherRefreshRuns removed latest run for school: %+v err=%v", status, err)
		}
	})
}

func TestMigrateLegacyMaintenanceReplayExpiration(t *testing.T) {
	repo, db := database(t)
	if _, err := db.Exec(`ALTER TABLE catalog.replays DROP COLUMN expires_at`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"catalog.corrected.v1", "catalog.merged.v1", "catalog.state_changed.v1", "catalog.imported.v1", "catalog.course_identities.v1", "catalog.enrollments.v1"} {
		if _, err := db.Exec(`INSERT INTO catalog.audit(id,data,audit_action) VALUES($1,'{}',$1)`, action); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO catalog.replays(key,data) VALUES($1,'{"Until":"2026-01-01T00:00:00Z"}')`, action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(postgres.Schema); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.PruneReplays(context.Background(), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 10); err != nil || n != 3 {
		t.Fatal("expected three expired maintenance receipts", n, err)
	}
	var retained int
	if err := db.QueryRow(`SELECT count(*) FROM catalog.replays WHERE key IN ('catalog.imported.v1','catalog.course_identities.v1','catalog.enrollments.v1') AND expires_at IS NULL`).Scan(&retained); err != nil || retained != 3 {
		t.Fatal(retained, err)
	}
}
