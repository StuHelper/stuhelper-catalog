package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

// This distribution mirrors the production catalog cardinalities without
// copying names, identifiers, accounts, enrollments or any other source data.
const directoryScaleSeed = `WITH sizes(kind,row_count) AS (VALUES
 ('course',21206),('teacher',22964),('term_course',56653),('class',133384),
 ('assignment',163283),('schedule',159448),('department',125),('term',73),
 ('category',63),('nature',5),('school',1)
), records AS (
 SELECT kind,n,kind||'-'||lpad(n::text,6,'0') AS id,
 CASE WHEN kind='course' THEN '工程数学课程' WHEN kind='teacher' THEN '合成教师' ELSE kind END || n AS name,
 CASE WHEN kind IN ('course','teacher','class','term_course') THEN 'department-'||lpad((n%125+1)::text,6,'0') ELSE '' END AS department_id,
 CASE WHEN kind IN ('class','term_course') THEN 'category-'||lpad((n%63+1)::text,6,'0') ELSE '' END AS category_id,
 CASE WHEN kind IN ('class','term_course') THEN 'nature-'||lpad((n%5+1)::text,6,'0') ELSE '' END AS nature_id,
 CASE WHEN kind='term_course' THEN 'course-'||lpad(((n-1)%21206+1)::text,6,'0') ELSE '' END AS course_id,
 CASE WHEN kind='term_course' THEN 'term-'||lpad(((n-1)/21206+1)::text,6,'0') ELSE '' END AS term_id,
 CASE WHEN kind='class' THEN 'term_course-'||lpad(((n-1)%56653+1)::text,6,'0') ELSE '' END AS term_course_id,
 CASE WHEN kind IN ('assignment','schedule') THEN 'class-'||lpad(((n-1)%133384+1)::text,6,'0') ELSE '' END AS class_id,
 CASE WHEN kind='assignment' THEN 'teacher-'||lpad(((n-1)%22964+1)::text,6,'0') ELSE '' END AS teacher_id
 FROM sizes CROSS JOIN LATERAL generate_series(1,sizes.row_count) AS seq(n)
), objects AS (
 SELECT *,jsonb_build_object('id',id,'kind',kind,'school_id','school-000001','name',name,'code','code-'||n,
 'department_id',department_id,'category_id',category_id,'nature_id',nature_id,'course_id',course_id,
 'term_id',term_id,'term_course_id',term_course_id,'class_id',class_id,'teacher_id',teacher_id,
 'aliases',jsonb_build_array('旧名称'||name)) AS object FROM records
)
INSERT INTO catalog.objects SELECT id,'school-000001',kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,jsonb_build_object('object',object) FROM objects;
INSERT INTO catalog.history SELECT id,1,data FROM catalog.objects;
ANALYZE catalog.objects; ANALYZE catalog.redirects;`

func TestDirectoryFilterProductionScale(t *testing.T) {
	repo, migration := database(t)
	// Exercise upgrading an existing production-sized table, not only new DDL.
	if _, err := migration.Exec(`DROP INDEX catalog.catalog_department_filter, catalog.catalog_category_filter, catalog.catalog_nature_filter, catalog.catalog_name_tokens, catalog.catalog_offering_course_lookup; DROP FUNCTION catalog.search_tokens(jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.Exec(directoryScaleSeed); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := postgres.Migrate(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	t.Logf("existing-table index migration: %s", time.Since(start))
	if err := postgres.Migrate(context.Background(), migration); err != nil {
		t.Fatal("migration replay", err)
	}
	var objects, history int
	if err := migration.QueryRow(`SELECT (SELECT count(*) FROM catalog.objects),(SELECT count(*) FROM catalog.history)`).Scan(&objects, &history); err != nil || objects != 557205 || history != 557205 {
		t.Fatal("migration changed business rows", objects, history, err)
	}
	// Deployment migrations run in a separate command that exits before serving
	// traffic. Close its analysis session before measuring the runtime pool.
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	repo.DB.SetMaxOpenConns(3)
	s := &c.Service{Repo: repo}
	ctx := context.Background()
	for _, kind := range []string{"course", "teacher"} {
		for _, name := range []string{"list", "department", "category", "nature", "combined", "department-category", "department-nature", "category-nature", "teacher", "teacher-combined", "short2-search", "short-search", "long-search", "missing-search", "combined-search", "missing-department"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				f := c.Filter{Kind: kind, SchoolID: "school-000001", Limit: 20}
				switch name {
				case "department":
					f.DepartmentID = "department-000003"
				case "category":
					f.CategoryID = "category-000003"
				case "nature":
					f.NatureID = "nature-000003"
				case "combined":
					f.DepartmentID = "department-000003"
					f.CategoryID = "category-000003"
					f.NatureID = "nature-000003"
				case "department-category":
					f.DepartmentID = "department-000003"
					f.CategoryID = "category-000003"
				case "department-nature":
					f.DepartmentID = "department-000003"
					f.NatureID = "nature-000003"
				case "category-nature":
					f.CategoryID = "category-000003"
					f.NatureID = "nature-000003"
				case "teacher":
					f.TeacherID = "teacher-000003"
				case "teacher-combined":
					f.TeacherID = "teacher-000003"
					f.DepartmentID = "department-000004"
					f.CategoryID = "category-000004"
					f.NatureID = "nature-000004"
				case "short2-search":
					f.Query = "数学"
				case "short-search":
					f.Query = "学"
				case "long-search":
					f.Query = "数学课"
				case "missing-search":
					f.Query = "不存在的合成名称"
				case "combined-search":
					f.DepartmentID = "department-000003"
					f.Query = "学"
				case "missing-department":
					f.DepartmentID = "department-not-found"
				}
				timings := make([]float64, 50)
				var mu sync.Mutex
				failures := 0
				var wg sync.WaitGroup
				for worker := 0; worker < 5; worker++ {
					wg.Go(func() {
						for i := worker; i < len(timings); i += 5 {
							call, cancel := context.WithTimeout(ctx, 3*time.Second)
							start := time.Now()
							page, err := s.List(call, f)
							timings[i] = float64(time.Since(start).Microseconds()) / 1000
							cancel()
							if err != nil || len(page.Items) > 20 {
								mu.Lock()
								failures++
								mu.Unlock()
							}
							for _, o := range page.Items {
								if o.SchoolID != f.SchoolID || o.Kind != kind {
									mu.Lock()
									failures++
									mu.Unlock()
								}
							}
						}
					})
				}
				wg.Wait()
				sort.Float64s(timings)
				t.Logf("557205 objects and history rows, 5 workers/3 connections, n=50 p95=%.3fms max=%.3fms failures=%d", timings[47], timings[49], failures)
				if failures != 0 {
					t.Fatalf("bounded indexed page failed %d requests", failures)
				}
			})
		}
	}
}

func TestDirectorySearchLiteralUnicodeAliasesAndMigration(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects, c.Object{ID: "literal", Kind: "course", SchoolID: "school", Name: "数学%_\\AB", Code: "literal-27", Aliases: []string{"量子力学", "旧名"}}, c.Object{ID: "boundaries", Kind: "course", SchoolID: "school", Name: "甲乙", Code: "丙丁", Aliases: []string{"戊己", "庚辛"}})
	if _, err := s.Import(ctx, p, "literal-seed", in); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"数", "数学", "%_\\a", "量子", "literal-27", "旧名"} {
		page, err := s.List(ctx, c.Filter{Kind: "course", Query: q, Limit: 20})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "literal" {
			t.Fatalf("literal %q: %+v %v", q, page, err)
		}
	}
	for _, q := range []string{"乙丙", "己庚", "甲丁", "数学_"} {
		page, err := s.List(ctx, c.Filter{Kind: "course", Query: q, Limit: 20})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("substring recheck %q: %+v %v", q, page, err)
		}
	}
	// Runtime identity can use the index function but cannot execute the migration.
	db := s.Repo.(*postgres.Repository).DB
	if err := postgres.Migrate(ctx, db); err == nil {
		t.Fatal("runtime executed migration")
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return c.Principal{Subject: "issuer:student"}, nil }}.Routes()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/catalog/objects?kind=course&q=literal-27&limit=1", nil))
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	var page c.Page
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "literal" {
		t.Fatal("HTTP query did not apply the search filter")
	}
	if strings.Contains(rr.Body.String(), "search_tokens") {
		t.Fatal("index leaked into public response")
	}
}

func TestDirectoryClassificationsCannotMixTeachingClasses(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	// Existing classes carry a different term/category combination. A new class
	// supplies only the other department; independently matching fields must not
	// turn into a matching combined row.
	in.Objects = append(in.Objects, c.Object{ID: "other-dept", Kind: "department", SchoolID: "school", Name: "Other"}, c.Object{ID: "other-category", Kind: "category", SchoolID: "school", Name: "Other"}, c.Object{ID: "other-class", Kind: "class", SchoolID: "school", Name: "Other", TermCourseID: "offering1", DepartmentID: "other-dept", CategoryID: "other-category"})
	if _, err := s.Import(ctx, p, "same-class-seed", in); err != nil {
		t.Fatal(err)
	}
	for _, f := range []c.Filter{{Kind: "course", DepartmentID: "other-dept", CategoryID: "seminar", Limit: 20}, {Kind: "course", DepartmentID: "dept", CategoryID: "other-category", Limit: 20}} {
		page, err := s.List(ctx, f)
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("mixed classifications: %+v %v", page, err)
		}
	}
	page, err := s.List(ctx, c.Filter{Kind: "course", DepartmentID: "other-dept", CategoryID: "other-category", Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(fmt.Sprint(page), err)
	}
}

func TestDirectoryListPoolDeadlineReturnsUnavailable(t *testing.T) {
	s, p, in := fixture(t)
	if _, err := s.Import(context.Background(), p, "pool-seed", in); err != nil {
		t.Fatal(err)
	}
	db := s.Repo.(*postgres.Repository).DB
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return c.Principal{Subject: "issuer:student"}, nil }}.Routes()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/catalog/objects?kind=course&limit=20", nil).WithContext(ctx))
	if rr.Code != 503 || rr.Body.String() != "{\"code\":\"unavailable\"}\n" {
		t.Fatalf("pool deadline response changed: %d %s", rr.Code, rr.Body.String())
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("test did not reach the pool queue")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(context.Background(), c.Filter{Kind: "course", Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("canceled wait damaged subsequent queries", err)
	}
}

func TestDirectorySearchPrefixFallsBackWithoutLosingPagination(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		name := "filler"
		if i < 19 || i == 180 || i == 190 {
			name = "popular"
		}
		in.Objects = append(in.Objects, c.Object{ID: fmt.Sprintf("prefix-%03d", i), Kind: "course", SchoolID: "school", Name: name})
	}
	if _, err := s.Import(ctx, p, "prefix-seed", in); err != nil {
		t.Fatal(err)
	}
	first, err := s.List(ctx, c.Filter{Kind: "course", Query: "popular", Limit: 20})
	if err != nil || len(first.Items) != 20 || first.Next != "prefix-180" {
		t.Fatalf("late match lost: %+v %v", first, err)
	}
	second, err := s.List(ctx, c.Filter{Kind: "course", Query: "popular", After: first.Next, Limit: 20})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "prefix-190" || second.Next != "" {
		t.Fatalf("cursor skipped late match: %+v %v", second, err)
	}
	frequent, err := s.List(ctx, c.Filter{Kind: "course", Query: "filler", Limit: 20})
	if err != nil || len(frequent.Items) != 20 || frequent.Items[0].ID != "prefix-019" || frequent.Next != "prefix-038" {
		t.Fatalf("prefix changed ordering: %+v %v", frequent, err)
	}
}
