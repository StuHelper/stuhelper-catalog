package catalog_test

import (
	"context"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"testing"
)

func TestDirectoryGroupsUseOnlyNamedOfferingDepartments(t *testing.T) {
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
	if _, err := s.RefreshTeachingDirectory(ctx, p, "school"); err != nil {
		t.Fatal(err)
	}
	reader := c.Principal{Subject: "issuer:student"}
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
		all, err := s.ReadDirectory(ctx, reader, q)
		if err != nil {
			t.Fatal(err)
		}
		if all.Total != 2 || len(all.DepartmentFacets) != 1 || all.DepartmentFacets[0].ID != "dept" || all.DepartmentFacets[0].Count != 1 {
			t.Fatalf("projected=%t total %d facets %+v", projected, all.Total, all.DepartmentFacets)
		}
		q.DepartmentID = "dept"
		group, err := s.ReadDirectory(ctx, reader, q)
		if err != nil || group.Total != 1 || len(group.Items) != 1 || group.Items[0].Object.ID != "course" || len(group.Items[0].Departments) != 1 || group.Items[0].Departments[0].ID != "dept" {
			t.Fatalf("projected=%t grouped rows %d %v", projected, group.Total, err)
		}
		q.DepartmentID = ""
		q.TeacherID = "teacher"
		filtered, err := s.ReadDirectory(ctx, reader, q)
		if err != nil || filtered.Total != 1 || len(filtered.DepartmentFacets) != 0 {
			t.Fatalf("projected=%t teacher-filtered mixed course %d %+v %v", projected, filtered.Total, filtered.DepartmentFacets, err)
		}
	}
}
