package catalog_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestMaintenanceImportReplaysPagesAndMatchesEagerAliases(t *testing.T) {
	ctx := context.Background()
	const subject = "maintenance-import-test"
	fetched := time.Now()
	interrupted := errors.New("stop after a committed page")
	type result struct {
		redirects []string
		objects   int
		replays   int
		events    int
	}

	run := func(t *testing.T, recover bool) result {
		t.Helper()
		s, p, admin, _ := projectionFixture(t)
		p.Subject = subject
		repo := s.Repo.(*postgres.Repository)
		if err := repo.CheckRuntime(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(`CREATE SCHEMA verification;
CREATE TABLE verification.private_grants (id text);
INSERT INTO catalog.redirects(alias_id,canonical_id,reason,created_at)
VALUES('old','course','synthetic',now()),('legacy','teacher','synthetic',now())`); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.DB.Exec(`SELECT id FROM verification.private_grants`); err == nil {
			t.Fatal("Catalog identity read Verification data")
		}
		if _, err := repo.DB.Exec(`CREATE TABLE catalog.unsafe_ddl(id text)`); err == nil {
			t.Fatal("Catalog identity executed DDL")
		}

		pages := []c.Import{
			{SchoolID: "school", SourceID: "maintenance-synthetic", Revision: 1, FetchedAt: fetched,
				Objects: []c.Object{{ID: "test-offering", Kind: "term_course", SchoolID: "school", Name: "Alias offering", CourseID: "old", TermID: "z-boundary"}}},
			{SchoolID: "school", SourceID: "maintenance-synthetic", Revision: 1, FetchedAt: fetched,
				Objects: []c.Object{{ID: "test-assignment", Kind: "assignment", SchoolID: "school", Name: "Alias assignment", ClassID: "class", TeacherID: "legacy"}}},
		}
		keys := []string{"same-batch/page-0", "same-batch/page-1"}
		readCount := func(db *sql.DB, statement string) int {
			t.Helper()
			var count int
			if err := db.QueryRowContext(ctx, statement).Scan(&count); err != nil {
				t.Fatal(err)
			}
			return count
		}
		if _, err := s.ImportDirectoryPage(ctx, p, keys[0], pages[0]); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("unfenced page was allowed: %v", err)
		}
		if recover {
			if published, err := s.CompletedDirectoryImport(ctx, p, "school", "same-batch"); err != nil || published != nil {
				t.Fatalf("unstarted import is reusable: %v, %v", published != nil, err)
			}
			_, err := s.RunDirectoryImport(ctx, p, "school", "same-batch", func(call context.Context) error {
				if _, err := s.Import(call, p, keys[0], pages[0]); err != nil {
					return err
				}
				return interrupted
			})
			if !errors.Is(err, interrupted) {
				t.Fatalf("interruption result: %v", err)
			}
			if published, err := s.CompletedDirectoryImport(ctx, p, "school", "same-batch"); err != nil || published != nil {
				t.Fatalf("interrupted import is reusable: %v, %v", published != nil, err)
			}
		}
		_, err := s.RunDirectoryImport(ctx, p, "school", "same-batch", func(call context.Context) error {
			for i, page := range pages {
				if recover {
					if _, err := s.ImportDirectoryPage(call, p, keys[i], page); err != nil {
						return err
					}
				} else if _, err := s.Import(call, p, keys[i], page); err != nil {
					return err
				}
			}
			if !recover {
				return nil
			}
			if got := readCount(admin, `SELECT count(*) FROM catalog.redirects WHERE alias_id='test-assignment'`); got != 0 {
				t.Fatalf("the deferred alias was written before the final reconciliation: %d", got)
			}
			invalid := p
			invalid.Schools = []string{"elsewhere"}
			if err := s.FinishDirectoryAliases(call, invalid, "school"); !errors.Is(err, c.ErrForbidden) {
				t.Fatalf("cross-school reconciliation passed: %v", err)
			}
			return s.FinishDirectoryAliases(call, p, "school")
		})
		if err != nil {
			t.Fatal(err)
		}
		if published, err := s.CompletedDirectoryImport(ctx, p, "school", "same-batch"); err != nil || published == nil {
			t.Fatalf("completed import is unavailable: %v, %v", published != nil, err)
		}
		if published, err := s.CompletedDirectoryImport(ctx, p, "school", "different-batch"); err != nil || published != nil {
			t.Fatalf("an unrelated batch is reusable: %v, %v", published != nil, err)
		}
		rows, err := admin.QueryContext(ctx, `SELECT alias_id || '=' || canonical_id
FROM catalog.redirects WHERE alias_id IN ('test-offering','test-assignment') ORDER BY alias_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out result
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out.redirects = append(out.redirects, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		out.objects = readCount(admin, `SELECT count(*) FROM catalog.objects WHERE id IN ('test-offering','test-assignment')`)
		out.replays = readCount(admin, `SELECT count(*) FROM catalog.replays`)
		out.events = readCount(admin, `SELECT count(*) FROM catalog.outbox`)
		return out
	}
	var eager, resumed result
	t.Run("eager", func(t *testing.T) { eager = run(t, false) })
	t.Run("resumed_with_deferral", func(t *testing.T) { resumed = run(t, true) })
	want := []string{"test-assignment=assignment", "test-offering=offering"}
	if !reflect.DeepEqual(eager.redirects, want) || !reflect.DeepEqual(eager, resumed) {
		t.Fatalf("eager and resumed publication differ: eager=%+v resumed=%+v", eager, resumed)
	}
}
