package catalog_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

type uiScaleAccess struct{}

func (uiScaleAccess) Scope(_ context.Context, p c.Principal, school string) (string, error) {
	if p.Subject == "" {
		return "", c.ErrUnauthenticated
	}
	if school != "" && school != "school-000001" {
		return "", c.ErrForbidden
	}
	return "school-000001", nil
}

type uiScaleMetrics map[string][]c.DirectoryMetric

func (m uiScaleMetrics) Metrics(_ context.Context, _ c.Principal, _, kind string, _, _ time.Time) ([]c.DirectoryMetric, error) {
	return m[kind], nil
}

func TestUIDirectoryProductionScale(t *testing.T) {
	repo, admin := database(t)
	ctx := context.Background()
	if _, err := admin.Exec(directoryScaleSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`INSERT INTO catalog.teacher_directory(teacher_id,school_id,teaching,updated_at) SELECT id,school_id,true,clock_timestamp() FROM catalog.objects WHERE kind='teacher' AND right(id,1)<>'0'`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`ANALYZE catalog.teacher_directory`); err != nil {
		t.Fatal(err)
	}
	metrics := uiScaleMetrics{}
	for kind, n := range map[string]int{"course": 21206, "teacher": 22964} {
		for i := 1; i <= n; i++ {
			metric := c.DirectoryMetric{ID: fmt.Sprintf("%s-%06d", kind, i), ReviewCount: int64(i % 17), RecentReviews: int64(i % 7), Favorites: int64(i % 5), Recommendation: []string{"unrated", "not_recommended", "average", "recommended", "strongly_recommended"}[i%5]}
			for d := range 5 {
				metric.Dimensions[d] = c.DirectoryBand{Level: i % 5, Samples: i % 17, Courses: 1}
			}
			metrics[kind] = append(metrics[kind], metric)
		}
	}
	repo.RequireTeacherDirectory = true
	repo.DB.SetMaxOpenConns(3)
	s := &c.Service{Repo: repo, ReadAccess: uiScaleAccess{}, DirectoryMetrics: metrics}
	p := c.Principal{Subject: "synthetic-benchmark-reader"}
	for _, tc := range []struct{ name, kind, sort, department, category, teacher, query string }{{"hot", "course", "hot", "", "", "", ""}, {"name", "course", "name", "", "", "", ""}, {"recommended", "course", "recommend", "", "", "", ""}, {"reviews", "course", "reviews", "", "", "", ""}, {"department-category", "course", "hot", "department-000003", "category-000003", "", ""}, {"short-search", "course", "hot", "", "", "", "学"}, {"teacher-filter", "course", "hot", "", "", "teacher-000003", ""}, {"marked-teachers", "teacher", "hot", "", "", "", ""}, {"teacher-department", "teacher", "reviews", "department-000003", "", "", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			q := c.DirectoryQuery{Filter: c.Filter{Kind: tc.kind, DepartmentID: tc.department, CategoryID: tc.category, TeacherID: tc.teacher, Query: tc.query, Limit: 20}, Sort: tc.sort}
			timings := make([]float64, 30)
			for i := range timings {
				start := time.Now()
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				page, err := s.ReadDirectory(call, p, q)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				timings[i] = float64(time.Since(start).Microseconds()) / 1000
				if len(page.Items) > 20 || page.Total < 0 {
					t.Fatal("bad page")
				}
			}
			sort.Float64s(timings)
			p95 := timings[28]
			t.Logf("557205 catalog rows + 557205 history; metric targets=%d; PostgreSQL 18.6; one worker / three-connection pool; n=30 p50=%.3fms p95=%.3fms max=%.3fms", len(metrics[tc.kind]), timings[15], p95, timings[29])
			if p95 > 300 {
				t.Errorf("directory list and facets p95 %.3fms exceeds 300ms", p95)
			}
		})
	}
}
