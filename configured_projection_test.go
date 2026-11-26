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

func TestConfiguredProjectionOldFormatKeepsNamedDepartmentPagination(t *testing.T) {
	s, p, db, old := projectionFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE catalog.projection_generations SET format_version=0 WHERE version=$1`, old.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE catalog.projection_objects SET department_sort_order=NULL WHERE version=$1 AND kind='department'`, old.Version); err != nil {
		t.Fatal(err)
	}
	current, err := s.ConfiguredProjectionCurrent(ctx, p, "school", old.Policy)
	if err != nil || current {
		t.Fatal("legacy format treated as current", err)
	}
	page, err := s.ReadList(ctx, p, c.Filter{Kind: "department", Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("legacy department unavailable", err)
	}
	_, _, done, err := s.AdvanceConfiguredProjection(ctx, p, "school", old.Policy)
	if err != nil || done {
		t.Fatal("format refresh not queued", err)
	}
	active, err := s.DirectoryProjection(ctx, p, "school")
	if err != nil || active.Version != old.Version {
		t.Fatal("legacy head lost", err)
	}
}

func TestConfiguredProjectionCanceledStatementDoesNotStopWriters(t *testing.T) {
	s, p, db, old := projectionFixture(t)
	ctx := context.Background()
	policy := old.Policy
	policy.HiddenCategoryNames = []string{"Visible category"}
	generation, _, _, err := s.AdvanceConfiguredProjection(ctx, p, "school", policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE FUNCTION catalog.hold_test_draft() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(551936); RETURN NULL; END $$;
 CREATE TRIGGER hold_test_draft BEFORE INSERT ON catalog.projection_objects FOR EACH STATEMENT EXECUTE FUNCTION catalog.hold_test_draft()`); err != nil {
		t.Fatal(err)
	}
	hold, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if _, err = hold.Exec("SELECT pg_advisory_xact_lock(551936)"); err != nil {
		t.Fatal(err)
	}
	build, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, _, _, e := s.AdvanceConfiguredProjection(build, p, "school", policy); finished <- e }()
	for {
		var waiting bool
		if err = db.QueryRow("SELECT EXISTS(SELECT FROM pg_locks WHERE locktype='advisory' AND objid=551936 AND NOT granted)").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if build.Err() != nil {
			t.Fatal("build never reached checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	write, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err = db.ExecContext(write, `SELECT pg_advisory_xact_lock(hashtextextended('catalog:projection-source:school',0)); UPDATE catalog.objects SET name=name WHERE id='course'`); err != nil {
		t.Fatal("business source write blocked", err)
	}
	if _, err = s.ReadSummary(write, p, "course"); err != nil {
		t.Fatal("old head read blocked", err)
	}
	cancel()
	if err = <-finished; err == nil {
		t.Fatal("canceled stage reported success")
	}
	var checkpoint, rows int
	if err = db.QueryRow(`SELECT build_step,(SELECT count(*) FROM catalog.projection_objects WHERE version=$1) FROM catalog.projection_generations WHERE version=$1`, generation).Scan(&checkpoint, &rows); err != nil || checkpoint != 0 || rows != 0 {
		t.Fatal("partial checkpoint committed", checkpoint, rows, err)
	}
}
