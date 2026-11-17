package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"slices"
	"testing"
	"time"
)

// Same 557205 source rows, membership and metrics on both sides. The first
// request is measured separately; p95 uses twenty subsequent requests per case.
func TestDirectoryProjectionProductionScaleBeforeAfter(t *testing.T) {
	repo, admin := database(t)
	ctx := context.Background()
	if _, err := admin.Exec(directoryScaleSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`INSERT INTO catalog.teacher_directory(teacher_id,school_id,teaching,updated_at) SELECT id,school_id,true,now() FROM catalog.objects WHERE kind='teacher'; ANALYZE catalog.teacher_directory`); err != nil {
		t.Fatal(err)
	}
	// Keep the source cardinality fixed while supplying explicit sports metadata.
	if _, err := admin.Exec(`UPDATE catalog.objects SET name=CASE id WHEN 'category-000001' THEN 'Synthetic sports' ELSE 'Synthetic sports alias' END,
 data=jsonb_set(data,'{object,name}',to_jsonb(CASE id WHEN 'category-000001' THEN 'Synthetic sports' ELSE 'Synthetic sports alias' END))
 WHERE id IN('category-000001','category-000002');
 UPDATE catalog.objects SET data=jsonb_set(data,'{object,sports_project_code}',to_jsonb('sport-'||((substring(id from 7)::integer/63)%3+1)::text))
 WHERE kind='class' AND category_id IN('category-000001','category-000002')`); err != nil {
		t.Fatal(err)
	}
	repo.RequireTeacherDirectory = true
	repo.DB.SetMaxOpenConns(3)
	repo.DB.SetMaxIdleConns(3)
	p := c.Principal{Subject: "synthetic-projection-benchmark", Schools: []string{"school-000001"}, CanImport: true}
	type measurement struct {
		FirstMS float64 `json:"first_ms"`
		P95MS   float64 `json:"p95_ms"`
		Count   int64   `json:"count"`
	}
	cases := []struct {
		name  string
		query c.DirectoryQuery
	}{
		{"course-hot", c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "hot"}},
		{"course-name", c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "name"}},
		{"course-recommend", c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "recommend"}},
		{"course-reviews", c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}, Sort: "reviews"}},
		{"teacher-list", c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 20}, Sort: "hot"}},
		{"teacher-filter", c.DirectoryQuery{Filter: c.Filter{Kind: "course", TeacherID: "teacher-000003", Limit: 20}, Sort: "hot"}},
		{"complex", c.DirectoryQuery{Filter: c.Filter{Kind: "course", DepartmentID: "department-000003", CategoryID: "category-000003", NatureID: "nature-000003", Query: "数学", Limit: 20}, Sort: "hot"}},
		{"options-counts", c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}}},
	}
	metrics := directoryScaleMetrics()
	measure := func(projected bool) map[string]measurement {
		out := map[string]measurement{}
		for _, tc := range cases {
			reader := &postgres.Repository{DB: repo.DB, RequireTeacherDirectory: true, RequireDirectoryProjection: projected}
			service := &c.Service{Repo: reader, ReadAccess: uiScaleAccess{}, DirectoryMetrics: metrics}
			m := measurement{}
			timings := []float64{}
			for i := 0; i < 21; i++ {
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				start := time.Now()
				if tc.name == "options-counts" {
					v, e := service.ReadFilterOptions(call, p, "course", "")
					if e != nil {
						cancel()
						t.Fatal(tc.name, e)
					}
					m.Count = v.Total
				} else {
					v, e := service.ReadDirectory(call, p, tc.query)
					if e != nil {
						cancel()
						t.Fatal(tc.name, e)
					}
					m.Count = v.Total
				}
				elapsed := float64(time.Since(start).Microseconds()) / 1000
				cancel()
				if i == 0 {
					m.FirstMS = elapsed
				} else {
					timings = append(timings, elapsed)
				}
			}
			slices.Sort(timings)
			m.P95MS = timings[18]
			out[tc.name] = m
		}
		return out
	}
	before := measure(false)
	s := &c.Service{Repo: repo}
	orders := []c.TermOrder{}
	for n := 1; n <= 73; n++ {
		year, season := 2020, "spring"
		if n == 1 {
			year, season = 2023, "autumn"
		} else if n == 2 {
			year, season = 2024, "spring"
		} else if n == 3 {
			year, season = 2024, "summer"
		}
		orders = append(orders, c.TermOrder{ID: fmt.Sprintf("term-%06d", n), Year: year, Season: season})
	}
	if err := s.SaveTermOrders(ctx, p, "school-000001", orders); err != nil {
		t.Fatal(err)
	}
	report, err := s.RebuildDirectory(ctx, p, "school-000001", nil)
	if err != nil {
		t.Fatal("rebuild", err)
	}
	after := measure(true)
	for name, b := range before {
		a := after[name]
		if a.Count != b.Count {
			t.Errorf("%s changed count from %d to %d", name, b.Count, a.Count)
		}
		if a.P95MS > b.P95MS*1.5+30 || (!projectionRaceBuild && a.P95MS > 300) {
			t.Errorf("%s regression before=%.3f after=%.3f ms", name, b.P95MS, a.P95MS)
		}
		if !projectionRaceBuild && a.FirstMS > 1500 {
			t.Errorf("%s cold projection query took %.3f ms", name, a.FirstMS)
		}
	}
	policy := c.DefaultDirectoryPolicy()
	policy.ExcludedCourseNames = []string{"工程数学课程1"}
	policy.CategoryAliases = []c.CategoryAlias{{From: "Synthetic sports alias", To: "Synthetic sports"}}
	policy.Sports = &c.SportsDirectoryPolicy{CategoryName: "Synthetic sports", Projects: []c.SportsProject{
		{Key: "one", Name: "Synthetic project one", SourceCodes: []string{"sport-1"}},
		{Key: "two", Name: "Synthetic project two", SourceCodes: []string{"sport-2"}},
		{Key: "three", Name: "Synthetic project three", SourceCodes: []string{"sport-3"}},
	}}
	displayReport, err := s.RebuildDirectory(ctx, p, "school-000001", &policy)
	if err != nil || displayReport.SportsProjects != 3 || displayReport.ExcludedCourses != 1 {
		t.Fatal("display projection", displayReport, err)
	}
	display := measure(true)
	if display["course-name"].Count != display["options-counts"].Count || display["course-name"].Count != displayReport.Courses {
		t.Fatal("display count mismatch", display)
	}
	for name, baseline := range after {
		if display[name].P95MS > baseline.P95MS*1.5+10 {
			t.Errorf("display rules regression %s before=%.3f after=%.3f ms", name, baseline.P95MS, display[name].P95MS)
		}
	}
	raw, err := json.Marshal(struct {
		Report        c.ProjectionReport `json:"report"`
		Before, After map[string]measurement
		DisplayReport c.ProjectionReport     `json:"display_report"`
		Display       map[string]measurement `json:"display"`
	}{report, before, after, displayReport, display})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("projection_race_build=%t projection_benchmark=%s", projectionRaceBuild, raw)
}
