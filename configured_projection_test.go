package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

// A checkpoint is committed with its rows. A new process can continue the same
// generation while all readers use the last complete one.
func TestConfiguredProjectionResumesAndPublishesAtomically(t *testing.T) {
	s, p, db, old := projectionFixture(t)
	ctx := context.Background()
	policy := old.Policy
	policy.ExcludedCourseNames = []string{"Current"}
	type publisher interface {
		AdvanceConfiguredProjection(context.Context, c.Principal, string, c.DirectoryPolicy) (int64, int, bool, error)
	}
	worker, ok := any(s).(publisher)
	if !ok {
		t.Fatal("resumable configured projection worker missing")
	}
	advance := func(ctx context.Context) (int64, int, bool, error) {
		return worker.AdvanceConfiguredProjection(ctx, p, "school", policy)
	}
	version, stage, done, err := advance(ctx)
	if err != nil || done || version <= old.Version {
		t.Fatalf("start: %d %d %v %v", version, stage, done, err)
	}
	next, stage2, _, err := advance(ctx)
	if err != nil || next != version || stage2 <= stage {
		t.Fatalf("checkpoint: %d %d %v", next, stage2, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, _, _, err = advance(canceled); err == nil {
		t.Fatal("canceled work succeeded")
	}
	// Inject an actual statement error in the unpublished generation.
	if _, err = db.Exec(`CREATE FUNCTION catalog.reject_test_generation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test interruption'; END $$; CREATE TRIGGER reject_test_generation BEFORE INSERT OR UPDATE OR DELETE ON catalog.projection_objects FOR EACH STATEMENT EXECUTE FUNCTION catalog.reject_test_generation()`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = advance(ctx); err == nil {
		t.Fatal("failed stage unexpectedly advanced")
	}
	active, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil || active.Version != old.Version {
		t.Fatal("old head lost", err)
	}
	if _, err = s.ReadSummary(ctx, p, "course"); err != nil {
		t.Fatal("old data not readable", err)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_test_generation ON catalog.projection_objects`); err != nil {
		t.Fatal(err)
	}
	restarted := &c.Service{Repo: s.Repo}
	worker = any(restarted).(publisher)
	next, stage, _, err = advance(ctx)
	if err != nil || next != version || stage != stage2+1 {
		t.Fatalf("resumed at %d/%d: %v", next, stage, err)
	}
	for n := 0; n < 50; n++ {
		_, _, done, err = advance(ctx)
		if err != nil {
			t.Fatal(err)
		}
		active, err = s.DirectoryProjection(ctx, p, "school")
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if active.Version != version {
				t.Fatal("wrong published generation")
			}
			break
		}
		if active.Version != old.Version {
			t.Fatal("intermediate generation published")
		}
	}
	if !done {
		t.Fatal("never published")
	}
	if _, err = s.ReadSummary(ctx, p, "course"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("new policy not active", err)
	}
	if _, _, done, err = advance(ctx); err != nil || !done {
		t.Fatal("unnecessary rebuild", err)
	}
}

func TestConfiguredBuildDoesNotLockSourceAndDiscardsStaleInput(t *testing.T) {
	s, p, db, old := projectionFixture(t)
	type publisher interface {
		AdvanceConfiguredProjection(context.Context, c.Principal, string, c.DirectoryPolicy) (int64, int, bool, error)
	}
	w, ok := any(s).(publisher)
	if !ok {
		t.Fatal("configured projection worker missing")
	}
	policy := old.Policy
	policy.HiddenCategoryNames = []string{"Visible category"}
	ctx := context.Background()
	first, _, _, err := w.AdvanceConfiguredProjection(ctx, p, "school", policy)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('catalog:projection-source:school',0))`); err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, n, _, err := w.AdvanceConfiguredProjection(call, p, "school", policy); err != nil || n < 1 {
		t.Fatal("draft stage conflicts with source writer", err)
	}
	if _, err = tx.Exec(`UPDATE catalog.objects SET name=name WHERE id='course'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		v, _, _, err := w.AdvanceConfiguredProjection(ctx, p, "school", policy)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 && v != first {
			return
		}
	}
	t.Fatal("stale source generation reused")
}
