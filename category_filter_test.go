package catalog_test

import (
	"encoding/json"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestDirectoryCategoryGroupingAndHiddenFiltersSurviveRebuild(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	ctx := t.Context()
	objects := []c.Object{
		{ID: "second-category", Kind: "category", Name: "Other category"},
		{ID: "hidden-category", Kind: "category", Name: "Hidden category"},
		{ID: "extra-class", Kind: "class", Name: "Extra class", TermCourseID: "offering", CategoryID: "second-category"},
		{ID: "hidden-course", Kind: "course", Name: "Hidden category course", CategoryID: "hidden-category", DepartmentID: "dept"},
		{ID: "hidden-offering", Kind: "term_course", Name: "Offering", CourseID: "hidden-course", TermID: "z-boundary"},
	}
	for i := range objects {
		objects[i].SchoolID = "school"
	}
	if _, err := s.Import(c.DeferDirectoryRebuild(ctx), p, "group-input", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: objects}); err != nil {
		t.Fatal(err)
	}
	policy := c.DefaultDirectoryPolicy()
	if err := json.Unmarshal([]byte(`{"category_aliases":[{"from":"Visible category","to":"合并分类","retire_source":true},{"from":"Other category","to":"合并分类","retire_source":true}],"hidden_category_names":["Hidden category"]}`), &policy); err != nil {
		t.Fatal(err)
	}
	for _, config := range []*c.DirectoryPolicy{&policy, nil} {
		if _, err := s.RebuildDirectory(ctx, p, "school", config); err != nil {
			t.Fatal(err)
		}
		query := c.DirectoryQuery{Filter: c.Filter{Kind: "course", Limit: 20}}
		active, err := s.ReadDirectory(ctx, p, query)
		if err != nil || active.Total != 2 || len(active.CategoryFacets) != 1 || active.CategoryFacets[0].Name != "合并分类" || active.CategoryFacets[0].Count != 1 {
			t.Fatalf("grouped categories and retained courses: total=%d facets=%+v err=%v", active.Total, active.CategoryFacets, err)
		}
		for _, id := range []string{"category", "second-category", "hidden-category", active.CategoryFacets[0].ID} {
			query.CategoryID = id
			page, err := s.ReadDirectory(ctx, p, query)
			want := int64(0)
			if id == active.CategoryFacets[0].ID {
				want = 1
			}
			if err != nil || page.Total != want {
				t.Fatalf("category %s: got %d want %d, err=%v", id, page.Total, want, err)
			}
		}
		query.CategoryID = ""
		query.DepartmentID = "dept"
		page, err := s.ReadDirectory(ctx, p, query)
		if err != nil || page.Total != 2 {
			t.Fatal("department must retain both courses", page.Total, err)
		}
	}
}
