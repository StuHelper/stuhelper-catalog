package catalog_test

import (
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"slices"
	"testing"
	"time"
)

func TestDepartmentOrderInDirectoriesFiltersAndPages(t *testing.T) {
	s, p, admin, _ := projectionFixture(t)
	ctx := t.Context()
	var objects []c.Object
	// Deliberately put names and IDs in different orders from the numeric codes.
	names := []string{"Zen", "Alpha", "Zulu", "Beta", "Cedar", "Gamma", "Oak", "Elm", "Pine"}
	codes := []string{"02", "30", "58", "60", "B22", "B100", "X1", "", ""}
	var want []string
	for i, code := range codes {
		id := fmt.Sprintf("d-%02d", len(codes)-i)
		want = append(want, id)
		co, off, cl, teacher := id+"-course", id+"-off", id+"-class", id+"-teacher"
		objects = append(objects, c.Object{ID: id, Kind: "department", Name: names[i], Code: code},
			c.Object{ID: co, Kind: "course", Name: co, DepartmentID: id}, c.Object{ID: off, Kind: "term_course", Name: off, CourseID: co, TermID: "z-boundary", DepartmentID: id},
			c.Object{ID: cl, Kind: "class", Name: cl, TermCourseID: off, DepartmentID: id}, c.Object{ID: teacher, Kind: "teacher", Name: teacher, DepartmentID: id}, c.Object{ID: id + "-assign", Kind: "assignment", Name: "Teaching", ClassID: cl, TeacherID: teacher})
	}
	for i := range objects {
		objects[i].SchoolID = "school"
		objects[i].Aliases = []string{}
	}
	if _, err := s.Import(c.DeferDirectoryRebuild(ctx), p, "dept-order", c.Import{SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: time.Now(), Objects: objects}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshTeachingDirectory(ctx, p, "school"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RebuildDirectory(ctx, p, "school", nil); err != nil {
		t.Fatal(err)
	}
	// Emulate a generation published before ranks existed, with an old cached
	// default page. Execute the real migration twice to cover upgrades and retries.
	if _, err := admin.Exec(`UPDATE catalog.projection_objects SET department_sort_order=NULL WHERE kind='department';
	UPDATE catalog.projection_selections SET data=jsonb_set(data,'{department_facets}',COALESCE((SELECT jsonb_agg(f ORDER BY f->>'name' COLLATE "C") FROM jsonb_array_elements(data->'department_facets') f),'[]'::jsonb))`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := admin.Exec(postgres.Schema); err != nil {
			t.Fatal(err)
		}
	}
	repo := s.Repo.(*postgres.Repository)
	check := func(got []string) {
		t.Helper()
		got = slices.DeleteFunc(got, func(id string) bool { return !slices.Contains(want, id) })
		if !slices.Equal(got, want) {
			t.Fatalf("department order got %v want %v", got, want)
		}
	}
	for _, projected := range []bool{true, false} {
		repo.RequireDirectoryProjection = projected
		for _, kind := range []string{"course", "teacher"} {
			dir, err := s.ReadDirectory(ctx, p, c.DirectoryQuery{Filter: c.Filter{Kind: kind, Limit: 2}})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range dir.DepartmentFacets {
				ids = append(ids, row.ID)
			}
			check(ids)
			opts, err := s.ReadFilterOptions(ctx, p, kind, "school")
			if err != nil {
				t.Fatal(err)
			}
			ids = nil
			for _, row := range opts.Departments {
				ids = append(ids, row.ID)
			}
			check(ids)
		}
		var ids []string
		after := ""
		for pageNo := 0; pageNo < 20; pageNo++ {
			page, err := s.ReadList(ctx, p, c.Filter{Kind: "department", After: after, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range page.Items {
				ids = append(ids, o.ID)
			}
			after = page.Next
			if after == "" {
				break
			}
		}
		if after != "" {
			t.Fatal("department pagination did not terminate")
		}
		check(ids)
	}
}
