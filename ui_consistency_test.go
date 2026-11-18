package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestUIDirectoryCacheInvalidatesMetadataAndTeacherMarkers(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	p := c.Principal{Subject: "issuer:student"}
	if _, err := s.Import(ctx, admin, "cache-seed", input); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	repo.RequireTeacherDirectory = true
	if _, err := s.RefreshTeachingDirectory(ctx, admin, "school"); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{rows: []c.DirectoryMetric{{ID: "course", ReviewCount: 3, Recommendation: "recommended"}}}
	q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "name"}
	for range 2 {
		v, err := s.ReadDirectory(ctx, p, q)
		if err != nil || v.Total != 1 || len(v.Items) != 1 {
			t.Fatal(v, err)
		}
	}
	s.DirectoryMetrics = uiMetrics{rows: []c.DirectoryMetric{{ID: "course", ReviewCount: 1, Recommendation: "unrated"}}}
	v, err := s.ReadDirectory(ctx, p, q)
	if err != nil || v.Items[0].ReviewCount != 1 || v.Items[0].Recommendation != "unrated" || v.CategoryFacets[0].ReviewCount != 1 {
		t.Fatal("review state was cached", v, err)
	}
	object, err := s.Get(ctx, "course")
	if err != nil {
		t.Fatal(err)
	}
	name := "Renamed course"
	if _, err := s.Correct(ctx, admin, "course", "rename-cache", c.Correction{Revision: object.Revision, Name: &name}); err != nil {
		t.Fatal(err)
	}
	v, err = s.ReadDirectory(ctx, p, q)
	if err != nil || v.Items[0].Object.Name != name {
		t.Fatal("metadata cache survived correction", v, err)
	}
	q.Kind = "teacher"
	v, err = s.ReadDirectory(ctx, p, q)
	if err != nil || v.Total != 2 {
		t.Fatal(v, err)
	}
	if _, err := repo.DB.Exec(`UPDATE catalog.teacher_directory SET teaching=false WHERE teacher_id='teacher'`); err != nil {
		t.Fatal(err)
	}
	v, err = s.ReadDirectory(ctx, p, q)
	if err != nil || v.Total != 1 || v.Items[0].Object.ID != "teacher2" {
		t.Fatal("T2 removal stayed cached", v, err)
	}
	q.Kind = "course"
	object, err = s.Get(ctx, "course")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetState(ctx, admin, "course", "hide-cache", c.ObjectState{Revision: object.Revision, Inactive: true, Reason: "Synthetic state change"}); err != nil {
		t.Fatal(err)
	}
	v, err = s.ReadDirectory(ctx, p, q)
	if err != nil || v.Total != 0 || len(v.Items) != 0 {
		t.Fatal("hidden course counted", v, err)
	}
}

func TestUIDirectoryConcurrentCacheAndCancellation(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, admin, "concurrent-cache", input); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{}
	q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "hot"}
	p := c.Principal{Subject: "issuer:student"}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 10 {
				page, err := s.ReadDirectory(ctx, p, q)
				if err != nil || page.Total != int64(len(page.Items)) {
					t.Errorf("concurrent cache: %+v %v", page, err)
					return
				}
			}
		})
	}
	wg.Wait()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReadDirectory(cancelled, p, q); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

type ownSummaryPermission struct{ err error }

func (a ownSummaryPermission) AuthorizeOwnSummary(context.Context, c.Principal, string) error {
	return a.err
}

func TestUINextClassDayAuthorizedSourceAndTimeBoundary(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }
	input.FetchedAt = now
	input.Objects = append(input.Objects, c.Object{ID: "ui-meeting", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Class", Location: "Room", TimeText: "周一 第1-2节 第1-2周"})
	if _, err := s.Import(ctx, admin, "next-class", input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutTimetable(ctx, admin, timetable()); err != nil {
		t.Fatal(err)
	}
	s.EnrollmentSource = sourceStub{snapshot: c.EnrollmentSnapshot{SchoolID: "school", SourceID: "approved-source", Version: "ui-batch", Revision: 2, FetchedAt: now, Enrollments: input.Enrollments}}
	p := c.Principal{Subject: "issuer:student"}
	got, err := s.MyNextClassDay(ctx, p)
	if err != nil || got.State != "unavailable" || len(got.Items) != 0 {
		t.Fatal("source alone opened enrollment display", got, err)
	}
	if _, err := s.MyReviewCandidates(ctx, p); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("pending courses bypassed display permission", err)
	}
	s.OwnSummaryAccess = ownSummaryPermission{}
	got, err = s.MyNextClassDay(ctx, p)
	if err != nil || got.State != "available" || got.Date == nil || *got.Date != "2026-09-07" || len(got.Items) != 1 || got.Items[0].StartPeriod != 1 || got.Items[0].EndPeriod != 2 || got.TimeZone != "Asia/Shanghai" {
		t.Fatalf("next day: %+v %v", got, err)
	}
	candidates, err := s.MyReviewCandidates(ctx, p)
	if err != nil || len(candidates.Items) == 0 || candidates.Owner != p.Subject {
		t.Fatal("authorized pending candidates unavailable", candidates, err)
	}
	s.OwnSummaryAccess = ownSummaryPermission{err: c.ErrForbidden}
	got, err = s.MyNextClassDay(ctx, p)
	if err != nil || got.State != "unavailable" || len(got.Items) != 0 {
		t.Fatal("revoked display permission ignored", got, err)
	}
	s.OwnSummaryAccess = ownSummaryPermission{}
	now = time.Date(2026, 9, 7, 1, 40, 0, 0, time.UTC)
	got, err = s.MyNextClassDay(ctx, p)
	if err != nil || got.Date == nil || *got.Date != "2026-09-14" {
		t.Fatal("ended class remained next", got, err)
	}
	now = time.Date(2026, 9, 14, 1, 40, 0, 0, time.UTC)
	got, err = s.MyNextClassDay(ctx, p)
	if err != nil || got.State != "empty" || got.Date != nil {
		t.Fatal("past calendar fabricated next class", got, err)
	}
	s.EnrollmentSource = sourceStub{err: c.ErrForbidden}
	got, err = s.MyNextClassDay(ctx, p)
	if err != nil || got.State != "unavailable" || len(got.Items) != 0 {
		t.Fatal("source authorization relaxed", got, err)
	}
}

func TestUIViewTokensAreDailyAndOldKeysExpire(t *testing.T) {
	s, admin, input := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }
	input.FetchedAt = now
	if _, err := s.Import(ctx, admin, "daily-views", input); err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	p := c.Principal{Subject: "issuer:student"}
	day := now
	for range 2 {
		if _, err := s.Read(ctx, p, "course"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.ReadActivity(ctx, c.ActivityID("course", "course", day))
	if err != nil || first.Metrics["views"] != 1 {
		t.Fatal("daily duplicate", first, err)
	}
	now = now.Add(24 * time.Hour)
	if _, err := s.Read(ctx, p, "course"); err != nil {
		t.Fatal(err)
	}
	var unique int
	if err := repo.DB.QueryRow(`SELECT count(DISTINCT digest) FROM catalog.view_dedup`).Scan(&unique); err != nil || unique != 2 {
		t.Fatal("token links two days", unique, err)
	}
	removed, err := s.PruneViewKeys(ctx, 1000)
	if err != nil || removed != 1 {
		t.Fatal("old key not deleted", removed, err)
	}
	second, err := repo.ReadActivity(ctx, c.ActivityID("course", "course", now))
	if err != nil || second.Metrics["views"] != 1 {
		t.Fatal("key cleanup changed aggregate", second, err)
	}
	if _, err := repo.PruneViewDedup(ctx, now, 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM catalog.view_dedup`).Scan(&count); err != nil || count != 1 {
		t.Fatal("old token retention", count, err)
	}
}

type countingReadAccess struct {
	calls atomic.Int64
}

func (a *countingReadAccess) Scope(_ context.Context, p c.Principal, requested string) (string, error) {
	a.calls.Add(1)
	if p.Subject == "" || requested != "" && requested != "school" {
		return "", c.ErrForbidden
	}
	return "school", nil
}

func TestUIDirectoryTeacherAliasAndPinnedDefaultCache(t *testing.T) {
	t.Run("teacher_alias_filter_and_single_scope_lookup", func(t *testing.T) {
		s, admin, in := fixture(t)
		ctx := context.Background()
		credits := 2.0
		in.Objects = append(in.Objects,
			c.Object{ID: "teacher-alias-old", Kind: "teacher", SchoolID: "school", DepartmentID: "dept", Name: "Merged Teacher Alias"},
			c.Object{ID: "course-second", Kind: "course", SchoolID: "school", DepartmentID: "dept", CategoryID: "seminar", Name: "Second Taught Course", Credits: &credits},
			c.Object{ID: "offering-second", Kind: "term_course", SchoolID: "school", CourseID: "course-second", TermID: "term1", Name: "Second Taught Course"},
			c.Object{ID: "class-second", Kind: "class", SchoolID: "school", TermCourseID: "offering-second", Name: "Second Class"},
			c.Object{ID: "assign-second", Kind: "assignment", SchoolID: "school", ClassID: "class-second", TeacherID: "teacher", Name: "Second Assignment"},
		)
		if _, err := s.Import(ctx, admin, "alias-directory-seed", in); err != nil {
			t.Fatal(err)
		}
		mergeIn := c.MergeInput{TargetID: "teacher", SourceRevision: 1, TargetRevision: 1, Reason: "merge duplicate teacher"}
		preview, err := s.PreviewMerge(ctx, admin, "teacher-alias-old", mergeIn)
		if err != nil {
			t.Fatal(err)
		}
		mergeIn.PreviewToken = preview.PreviewToken
		if _, err := s.Merge(ctx, admin, "teacher-alias-old", "merge-teacher-alias", mergeIn); err != nil {
			t.Fatal(err)
		}
		repo := s.Repo.(*postgres.Repository)
		repo.RequireTeacherDirectory = true
		if _, err := s.RefreshTeachingDirectory(ctx, admin, "school"); err != nil {
			t.Fatal(err)
		}
		access := &countingReadAccess{}
		s.ReadAccess = access
		s.DirectoryMetrics = uiMetrics{}
		student := c.Principal{Subject: "issuer:student"}

		access.calls.Store(0)
		firstPage, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{
			Filter: c.Filter{Kind: "course", TeacherID: "teacher-alias-old", Limit: 1},
			Sort:   "name",
		})
		if err != nil {
			t.Fatal(err)
		}
		if access.calls.Load() != 1 {
			t.Fatalf("ReadDirectory with teacher_id called ReadAccess.Scope %d times, want 1", access.calls.Load())
		}
		if firstPage.Total != 2 || len(firstPage.Items) != 1 || firstPage.Next == "" {
			t.Fatalf("ReadDirectory missed courses taught by canonical teacher via alias: %+v", firstPage)
		}
		secondPage, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{
			Filter: c.Filter{Kind: "course", TeacherID: "teacher", After: firstPage.Next, Limit: 1},
			Sort:   "name",
		})
		if err != nil || secondPage.Total != 2 || len(secondPage.Items) != 1 || secondPage.Next != "" {
			t.Fatalf("alias cursor did not match canonical teacher pagination: %+v err=%v", secondPage, err)
		}
	})

	t.Run("pinned_unfiltered_default_selections_survive_filtered_churn", func(t *testing.T) {
		s, p, adminDB, _ := projectionFixture(t)
		ctx := context.Background()
		student := c.Principal{Subject: "issuer:student"}

		courses, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "hot"})
		if err != nil || courses.Total != 1 {
			t.Fatalf("warm default courses: %+v err=%v", courses, err)
		}
		teachers, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 20}, Sort: "hot"})
		if err != nil || teachers.Total != 2 {
			t.Fatalf("warm default teachers: %+v err=%v", teachers, err)
		}

		for i := range 40 {
			q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Query: fmt.Sprintf("filtered-churn-%02d", i), Limit: 20}, Sort: "hot"}
			if _, err := s.ReadDirectory(ctx, student, q); err != nil {
				t.Fatal(err)
			}
		}

		// Delete prebuilt selection rows without changing projection_heads.version.
		// If default unfiltered selections stayed pinned in memory, both reads still succeed.
		if _, err := adminDB.ExecContext(ctx, `DELETE FROM catalog.projection_selections`); err != nil {
			t.Fatal(err)
		}
		coursesAfter, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "name"})
		if err != nil || coursesAfter.Total != 1 || len(coursesAfter.Items) != 1 {
			t.Fatalf("default course selection was evicted by filtered query churn: %+v err=%v", coursesAfter, err)
		}
		teachersAfter, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 20}, Sort: "name"})
		if err != nil || teachersAfter.Total != 2 || len(teachersAfter.Items) != 2 {
			t.Fatalf("default teacher selection was evicted by filtered query churn: %+v err=%v", teachersAfter, err)
		}

		// Rebuilding advances the projection version and refreshes pinned entries from the new generation.
		policy := c.DirectoryPolicy{CutoffYear: 2023, CutoffSeason: "autumn", ExcludedCourseNames: []string{"Current"}}
		if _, err := s.RebuildDirectory(ctx, p, "school", &policy); err != nil {
			t.Fatal(err)
		}
		rebuiltCourses, err := s.ReadDirectory(ctx, student, c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "hot"})
		if err != nil || rebuiltCourses.Total != 0 {
			t.Fatalf("pinned cache did not invalidate on new projection version: %+v err=%v", rebuiltCourses, err)
		}
	})
}
