package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) FilterOptions(kind, school string) (c.FilterOptions, error) {
	out := c.FilterOptions{Kind: kind, Departments: []c.FilterOption{}, Categories: []c.FilterOption{}, Natures: []c.FilterOption{}}
	if kind == "teacher" && !t.requireTeacherDirectory {
		return out, c.ErrUnavailable
	}
	// Revision and members must describe the same snapshot, including cache hits.
	if _, err := t.tx.ExecContext(t.ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return out, safe(err)
	}
	if _, err := t.tx.ExecContext(t.ctx, "SET LOCAL jit=off"); err != nil {
		return out, safe(err)
	}
	if t.projectionRead {
		return t.projectedFilterOptions(kind, school)
	}
	selection, err := t.directorySelection(c.DirectoryQuery{Filter: c.Filter{Kind: kind, SchoolID: school}})
	if err != nil {
		return out, err
	}
	out.Total = int64(len(selection.Candidates))
	copyNonempty := func(facets []c.DirectoryFacet) []c.FilterOption {
		items := []c.FilterOption{}
		for _, f := range facets {
			if f.ID != "" && f.Name != "" && f.Count > 0 {
				items = append(items, c.FilterOption{ID: f.ID, Name: f.Name, Count: f.Count})
			}
		}
		return items
	}
	out.Departments = copyNonempty(selection.DepartmentFacets)
	if kind == "course" {
		out.Categories = copyNonempty(selection.CategoryFacets)
		out.Natures = copyNonempty(selection.NatureFacets)
	}
	return out, nil
}
