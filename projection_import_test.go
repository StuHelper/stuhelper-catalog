package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestDirectoryProjectionRetiredImportCannotWriteOrPublish(t *testing.T) {
	for _, replacement := range []string{"same-batch", "different-batch"} {
		t.Run(replacement, func(t *testing.T) {
			s, p, _, _ := projectionFixture(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s.Clock = func() time.Time { return now }
			var current c.ProjectionReport
			_, err := s.RunDirectoryImport(ctx, p, "school", "same-batch", func(retired context.Context) error {
				retired = context.WithoutCancel(retired)
				// A stalled execution outlives its lease. Retrying the same source
				// batch must still fence the earlier execution.
				now = now.Add(3 * time.Hour)
				var err error
				current, err = s.RunDirectoryImport(ctx, p, "school", replacement, func(context.Context) error { return nil })
				if err != nil {
					return err
				}
				_, err = s.Import(retired, p, "late-page", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: now,
					Objects: []c.Object{{ID: "late-course", Kind: "course", SchoolID: "school", Name: "Retired execution"}}})
				if !errors.Is(err, c.ErrConflict) {
					t.Errorf("retired execution wrote a source page: %v", err)
				}
				return nil
			})
			if !errors.Is(err, c.ErrConflict) {
				t.Fatal("retired completion changed the error or replacement state", err)
			}
			if _, err = s.AdminRead(ctx, p, "late-course"); !errors.Is(err, c.ErrNotFound) {
				t.Fatal("retired page changed stored source data", err)
			}
			after, err := s.DirectoryProjection(ctx, p, "school")
			if err != nil || after.Version != current.Version {
				t.Fatal("retired execution changed the published version", after, err)
			}
		})
	}
}

func TestDirectoryProjectionCompletedAndExpiredImportsRejectFurtherPages(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "expired"}[expired], func(t *testing.T) {
			s, p, admin, _ := projectionFixture(t)
			ctx := context.Background()
			var retired context.Context
			write := func() error {
				_, err := s.Import(retired, p, "late-page", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(),
					Objects: []c.Object{{ID: "late-course", Kind: "course", SchoolID: "school", Name: "Invalid execution"}}})
				return err
			}
			_, err := s.RunDirectoryImport(ctx, p, "school", "batch", func(call context.Context) error {
				retired = context.WithoutCancel(call)
				if expired {
					if _, err := admin.Exec(`UPDATE catalog.projection_imports SET deadline=now()-interval '1 second' WHERE school_id='school'`); err != nil {
						return err
					}
					if err := write(); !errors.Is(err, c.ErrConflict) {
						t.Errorf("expired execution wrote a source page: %v", err)
					}
					return c.ErrConflict
				}
				return nil
			})
			if expired && !errors.Is(err, c.ErrConflict) || !expired && err != nil {
				t.Fatal(err)
			}
			if !expired {
				if err := write(); !errors.Is(err, c.ErrConflict) {
					t.Errorf("completed execution wrote a source page: %v", err)
				}
			}
			if _, err = s.AdminRead(ctx, p, "late-course"); !errors.Is(err, c.ErrNotFound) {
				t.Fatal("invalid execution changed stored source data", err)
			}
		})
	}
}
