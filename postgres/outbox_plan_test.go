package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
)

// Exercise the exact production SQL with the normal planner and a realistic
// sent/pending ratio. Do not disable sequential scans to manufacture an index plan.
func TestPendingOutboxExclusionUsesPartialIndex(t *testing.T) {
	dsn := os.Getenv("STUHELPER_CATALOG_TEST_DSN")
	if dsn == "" {
		t.Fatal("isolated PostgreSQL test DSN is required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal("invalid test DSN")
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	random := make([]byte, 8)
	if _, e = rand.Read(random); e != nil {
		t.Fatal(e)
	}
	name := "catalog_outbox_plan_" + hex.EncodeToString(random)
	if _, e = admin.Exec(`CREATE DATABASE ` + name); e != nil {
		t.Fatal(e)
	}
	cfg.Database = name
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		db.Close()
		if _, e := admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`); e != nil {
			t.Error(e)
		}
	})
	if _, e = db.Exec(Schema); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO catalog.outbox(id,data,sent_at)
 SELECT 'event-'||n,jsonb_build_object('source','catalog','subject',CASE WHEN n<=100 THEN 'a-blocked' ELSE 'z-ready' END,'data',jsonb_build_object('revision',n)),CASE WHEN n>200 THEN now() ELSE NULL END FROM generate_series(1,20200) n;
 ANALYZE catalog.outbox`); e != nil {
		t.Fatal(e)
	}
	maximum := make([]string, 1000)
	for i := range maximum {
		maximum[i] = fmt.Sprintf("absent-%d", i)
	}
	maximum[0] = "a-blocked"
	cases := []struct {
		name     string
		excluded []string
		rows     int
	}{{"nil", nil, 100}, {"empty", []string{}, 100}, {"blocked", []string{"a-blocked"}, 100}, {"all", []string{"a-blocked", "z-ready"}, 0}, {"maximum", maximum, 100}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if e := db.QueryRowContext(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+pendingOutboxSQL, 100, tc.excluded).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			var plan []struct{ Plan map[string]any }
			if e := json.Unmarshal(raw, &plan); e != nil || len(plan) != 1 {
				t.Fatal(string(raw), e)
			}
			indexed := false
			var walk func(map[string]any)
			walk = func(node map[string]any) {
				if node["Node Type"] == "Seq Scan" {
					t.Fatal("full table scan", string(raw))
				}
				if node["Index Name"] == "catalog_outbox_pending" {
					indexed = true
				}
				if children, ok := node["Plans"].([]any); ok {
					for _, child := range children {
						walk(child.(map[string]any))
					}
				}
			}
			walk(plan[0].Plan)
			if !indexed || plan[0].Plan["Actual Rows"] != float64(tc.rows) {
				t.Fatal("unexpected pending query plan", string(raw))
			}
		})
	}
}
