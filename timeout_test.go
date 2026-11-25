package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
)

type blockingRecentResources struct{}

func (blockingRecentResources) CountRecentResources(ctx context.Context, _ c.Principal, _ string, _ []string) (map[string]int64, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingDirectoryMetrics struct{}

func (blockingDirectoryMetrics) Metrics(ctx context.Context, _ c.Principal, _, _ string) ([]c.DirectoryMetric, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingOwnSummaryAccess struct{}

func (blockingOwnSummaryAccess) AuthorizeOwnSummary(ctx context.Context, _ c.Principal, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

type blockingEnrollmentSource struct{}

func (blockingEnrollmentSource) OwnSnapshot(ctx context.Context, _ string) (c.EnrollmentSnapshot, error) {
	<-ctx.Done()
	return c.EnrollmentSnapshot{}, ctx.Err()
}

type allowOwnSummaryAccess struct{}

func (allowOwnSummaryAccess) AuthorizeOwnSummary(context.Context, c.Principal, string) error {
	return nil
}

func TestExternalPortSubTimeoutsHTTPWriteBudgetAndRebuildLockTimeout(t *testing.T) {
	t.Run("external_sub_timeouts_and_degradation", func(t *testing.T) {
		s, admin, in := fixture(t)
		ctx := context.Background()
		if _, err := s.Import(ctx, admin, "seed-timeouts", in); err != nil {
			t.Fatal(err)
		}
		student := c.Principal{Subject: "issuer:student"}
		if err := s.RecordRecentCourse(ctx, student, "course"); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup

		// 1. RecentResources sub-timeout degrades gracefully (keeping visited items) when parent ctx is still valid.
		wg.Go(func() {
			sub := &c.Service{Repo: s.Repo, ReadAccess: s.ReadAccess, Clock: s.Clock, RecentResources: blockingRecentResources{}}
			parentCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			defer cancel()
			started := time.Now()
			recent, err := sub.MyRecentCourses(parentCtx, student)
			elapsed := time.Since(started)
			if err != nil || parentCtx.Err() != nil {
				t.Errorf("MyRecentCourses failed instead of degrading: err=%v parentErr=%v elapsed=%s", err, parentCtx.Err(), elapsed)
				return
			}
			if elapsed >= 4*time.Second || recent.ResourcesState != "unavailable" || len(recent.Items) != 1 || recent.Items[0].Course.ID != "course" || recent.Items[0].ResourceCount != nil {
				t.Errorf("MyRecentCourses unexpected degradation: elapsed=%s recent=%+v", elapsed, recent)
			}
			canceledCtx, cancelNow := context.WithCancel(ctx)
			cancelNow()
			if _, err := sub.MyRecentCourses(canceledCtx, student); !errors.Is(err, context.Canceled) {
				t.Errorf("MyRecentCourses swallowed caller cancellation: %v", err)
			}
		})

		// 2. DirectoryMetrics sub-timeout returns ErrUnavailable before parent deadline.
		wg.Go(func() {
			sub := &c.Service{Repo: s.Repo, ReadAccess: s.ReadAccess, Clock: s.Clock, DirectoryMetrics: blockingDirectoryMetrics{}}
			parentCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			defer cancel()
			started := time.Now()
			_, err := sub.ReadDirectory(parentCtx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "hot"})
			elapsed := time.Since(started)
			if !errors.Is(err, c.ErrUnavailable) || parentCtx.Err() != nil || elapsed >= 6*time.Second {
				t.Errorf("ReadDirectory sub-timeout failed: err=%v parentErr=%v elapsed=%s", err, parentCtx.Err(), elapsed)
			}
		})

		// 3. OwnSummaryAccess sub-timeout degrades MyNextClassDay to unavailable and bounds MyReviewCandidates.
		wg.Go(func() {
			sub := &c.Service{Repo: s.Repo, ReadAccess: s.ReadAccess, Clock: s.Clock, OwnSummaryAccess: blockingOwnSummaryAccess{}, EnrollmentSource: blockingEnrollmentSource{}}
			parentCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			defer cancel()
			started := time.Now()
			day, err := sub.MyNextClassDay(parentCtx, student)
			elapsed := time.Since(started)
			if err != nil || parentCtx.Err() != nil || elapsed >= 5*time.Second || day.State != "unavailable" || day.Reason != "enrollment_unavailable" {
				t.Errorf("MyNextClassDay sub-timeout failed: day=%+v err=%v parentErr=%v elapsed=%s", day, err, parentCtx.Err(), elapsed)
			}
			if _, err := sub.MyReviewCandidates(parentCtx, student); !errors.Is(err, c.ErrUnavailable) || parentCtx.Err() != nil {
				t.Errorf("MyReviewCandidates sub-timeout failed: err=%v parentErr=%v", err, parentCtx.Err())
			}
		})

		// 4. EnrollmentSource sub-timeout bounds own enrollment reads and degrades MyNextClassDay.
		wg.Go(func() {
			sub := &c.Service{Repo: s.Repo, ReadAccess: s.ReadAccess, Clock: s.Clock, OwnSummaryAccess: allowOwnSummaryAccess{}, EnrollmentSource: blockingEnrollmentSource{}}
			parentCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			started := time.Now()
			_, err := sub.MyEnrollments(parentCtx, student, "school", "", "", "", 20)
			elapsed := time.Since(started)
			if !errors.Is(err, c.ErrUnavailable) || parentCtx.Err() != nil || elapsed >= 7*time.Second {
				t.Errorf("MyEnrollments sub-timeout failed: err=%v parentErr=%v elapsed=%s", err, parentCtx.Err(), elapsed)
			}
			canceledCtx, cancelNow := context.WithCancel(ctx)
			cancelNow()
			if _, err := sub.MyEnrollments(canceledCtx, student, "school", "", "", "", 20); !errors.Is(err, context.Canceled) {
				t.Errorf("MyEnrollments swallowed caller cancellation: %v", err)
			}
		})

		wg.Wait()
	})

	t.Run("http_route_timeout_budgets", func(t *testing.T) {
		s, admin, _ := fixture(t)
		var observedRemaining time.Duration
		h := httpapi.Handler{
			Service: s,
			Authenticate: func(req *http.Request) (c.Principal, error) {
				dl, ok := req.Context().Deadline()
				if !ok {
					t.Errorf("%s %s missing request deadline", req.Method, req.URL.Path)
				} else {
					observedRemaining = time.Until(dl)
				}
				return admin, nil
			},
		}.Routes()

		checkBudget := func(method, path, body string, wantWriteBudget bool) {
			t.Helper()
			observedRemaining = 0
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Idempotency-Key", "budget-check-key")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if wantWriteBudget {
				if observedRemaining <= 30*time.Second {
					t.Fatalf("%s %s remaining budget=%s, want >30s maintenance write budget", method, path, observedRemaining)
				}
			} else {
				if observedRemaining <= 0 || observedRemaining > 16*time.Second {
					t.Fatalf("%s %s remaining budget=%s, want <=15s read/preview budget", method, path, observedRemaining)
				}
			}
		}

		for _, tc := range []struct{ method, path, body string }{
			{http.MethodPatch, "/api/v1/catalog/objects/course", `{"revision":1,"reason":"fix"}`},
			{http.MethodPost, "/api/v1/catalog/objects/course/state", `{"revision":1,"inactive":true,"reason":"hide"}`},
			{http.MethodPost, "/api/v1/catalog/objects/course/merge", `{"target_id":"other","source_revision":1,"target_revision":1,"preview_token":"tok","reason":"merge"}`},
			{http.MethodPost, "/api/v1/catalog/imports", `{}`},
			{http.MethodPost, "/api/v1/catalog/imports/confirm", `{}`},
			{http.MethodPost, "/api/v1/catalog/course-identities", `{}`},
			{http.MethodPost, "/api/v1/catalog/admin/schools/school/teacher-sites/refresh", `{"mode":"official"}`},
			{http.MethodPut, "/api/v1/catalog/admin/schools/school/terms/term1/timetable", `{}`},
		} {
			checkBudget(tc.method, tc.path, tc.body, true)
		}

		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/catalog/objects/course", ""},
			{http.MethodGet, "/api/v1/catalog/directory?kind=course", ""},
			{http.MethodPost, "/api/v1/catalog/objects/course/merge-preview", `{"target_id":"other","source_revision":1,"target_revision":1,"reason":"merge"}`},
			{http.MethodPost, "/api/v1/catalog/imports/preview", `{}`},
			{http.MethodPut, "/api/v1/catalog/me/recent-courses/course", ""},
			{http.MethodDelete, "/api/v1/catalog/me/recent-courses", ""},
		} {
			checkBudget(tc.method, tc.path, tc.body, false)
		}
	})

	t.Run("rebuild_lock_timeout_precedes_advisory_locks", func(t *testing.T) {
		s, p, adminDB, first := projectionFixture(t)
		ctx := context.Background()
		tx, err := adminDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('catalog:projection-source:school',0))`); err != nil {
			t.Fatal(err)
		}

		callCtx, cancel := context.WithTimeout(ctx, 13*time.Second)
		defer cancel()
		_, err = s.RebuildDirectory(callCtx, p, "school", nil)
		if !errors.Is(err, c.ErrUnavailable) || callCtx.Err() != nil {
			t.Fatalf("RebuildDirectory did not fail via SQL lock_timeout before caller deadline: err=%v ctxErr=%v", err, callCtx.Err())
		}
		var version int64
		if err := adminDB.QueryRow(`SELECT version FROM catalog.projection_heads WHERE school_id='school'`).Scan(&version); err != nil || version != first.Version {
			t.Fatalf("published projection version changed on lock timeout: version=%d want=%d err=%v", version, first.Version, err)
		}
		var reason string
		if err := adminDB.QueryRow(`SELECT reason FROM catalog.projection_failures WHERE school_id='school'`).Scan(&reason); err != nil || reason != "lock_timeout" {
			t.Fatalf("projection_failures not recorded as lock_timeout: reason=%q err=%v", reason, err)
		}
	})
}
