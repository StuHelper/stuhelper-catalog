package catalog_test

import (
	"context"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"testing"
)

func TestUnassignedDepartmentIsExclusiveAcrossDirectoryFilters(t *testing.T) {
	s, p, input := fixture(t)
	for i := range input.Objects {
		switch input.Objects[i].ID {
		case "course":
			input.Objects[i].DepartmentID = ""
		case "class2":
			input.Objects[i].DepartmentID = "dept"
		}
	}
	input.Objects = append(input.Objects,
		c.Object{ID: "unassigned", Kind: "course", SchoolID: "school", Name: "Unassigned"},
		c.Object{ID: "unassigned-offering", Kind: "term_course", SchoolID: "school", Name: "Offering", CourseID: "unassigned", TermID: "term1"})
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "dirgroup-seed", input); err != nil {
		t.Fatal(err)
	}
	s.DirectoryMetrics = uiMetrics{rows: []c.DirectoryMetric{{ID: "unassigned", ReviewCount: 3, Recommendation: "unrated"}}}
	repo := s.Repo.(*postgres.Repository)
	repo.RequireTeacherDirectory = true
	for _, projected := range []bool{false, true} {
		if projected {
			if err := s.SaveTermOrders(ctx, p, "school", []c.TermOrder{{ID: "term1", Year: 2025, Season: "spring"}, {ID: "term2", Year: 2025, Season: "autumn"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RebuildDirectory(ctx, p, "school", nil); err != nil {
				t.Fatal(err)
			}
			repo.RequireDirectoryProjection = true
		}
		q := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 10}}
		all, err := s.ReadDirectory(ctx, p, q)
		if err != nil {
			t.Fatal(err)
		}
		if all.Total != 2 || len(all.DepartmentFacets) != 2 {
			t.Fatalf("projected=%t totals %d facets %+v", projected, all.Total, all.DepartmentFacets)
		}
		last := all.DepartmentFacets[1]
		if last.ID != "@unassigned" || last.Name == "" || last.Count != 1 || last.ReviewCount != 3 {
			t.Fatalf("projected=%t unassigned facet %+v", projected, last)
		}
		q.DepartmentID = "@unassigned"
		group, err := s.ReadDirectory(ctx, p, q)
		if err != nil || group.Total != 1 || len(group.Items) != 1 || group.Items[0].Object.ID != "unassigned" || len(group.Items[0].Departments) != 0 {
			t.Fatalf("projected=%t unassigned group total %d err %v", projected, group.Total, err)
		}
		q.DepartmentID = ""
		q.TeacherID = "teacher"
		filtered, err := s.ReadDirectory(ctx, p, q)
		if err != nil || filtered.Total != 1 || len(filtered.DepartmentFacets) != 0 {
			t.Fatalf("projected=%t teacher-filtered mixed course %d %+v %v", projected, filtered.Total, filtered.DepartmentFacets, err)
		}
	}
}
