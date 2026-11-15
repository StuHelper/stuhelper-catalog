package catalog_test

import (
	"context"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"sort"
	"testing"
	"time"
)

func TestFilterOptionsNonzeroCountsAndRevisionInvalidation(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	reader := c.Principal{Subject: "issuer:student"}
	for _, k := range []string{"department", "category", "nature"} {
		in.Objects = append(in.Objects, c.Object{ID: "empty-" + k, Kind: k, SchoolID: "school", Name: "Unused " + k})
	}
	for i := range in.Objects {
		if in.Objects[i].Kind == "teacher" {
			in.Objects[i].DepartmentID = "dept"
		}
	}
	in.Objects = append(in.Objects, c.Object{ID: "unmarked", Kind: "teacher", SchoolID: "school", DepartmentID: "empty-department", Name: "Unmarked"})
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	repo := s.Repo.(*postgres.Repository)
	repo.RequireTeacherDirectory = true
	s.DirectoryMetrics = uiMetrics{}
	// Reproduce the former choice source: generic taxonomy enumeration includes
	// an unused category even though the course list for it is empty.
	old, e := s.ReadList(ctx, reader, c.Filter{Kind: "category", Limit: 100})
	if e != nil || len(old.Items) != 2 {
		t.Fatal(old, e)
	}
	empty, e := s.ReadList(ctx, reader, c.Filter{Kind: "course", CategoryID: "empty-category", Limit: 100})
	if e != nil || len(empty.Items) != 0 {
		t.Fatal(empty, e)
	}
	check := func(kind string) c.FilterOptions {
		t.Helper()
		v, e := s.ReadFilterOptions(ctx, reader, kind, "")
		if e != nil {
			t.Fatal(e)
		}
		directory, e := s.ReadDirectory(ctx, reader, c.DirectoryQuery{Filter: c.Filter{Kind: kind, Limit: 100}, Sort: "name"})
		if e != nil || directory.Total != v.Total {
			t.Fatal("directory total", v, directory, e)
		}
		for group, options := range map[string][]c.FilterOption{"department": v.Departments, "category": v.Categories, "nature": v.Natures} {
			for _, option := range options {
				if option.Count <= 0 {
					t.Fatal("zero option", option)
				}
				filter := c.Filter{Kind: kind, Limit: 100}
				switch group {
				case "department":
					filter.DepartmentID = option.ID
				case "category":
					filter.CategoryID = option.ID
				case "nature":
					filter.NatureID = option.ID
				}
				page, e := s.ReadList(ctx, reader, filter)
				if e != nil || int64(len(page.Items)) != option.Count {
					t.Fatal("option/list count", kind, option, page, e)
				}
			}
		}
		return v
	}
	for range 2 {
		v := check("course")
		if v.Total != 1 || len(v.Departments) != 1 || len(v.Categories) != 1 || len(v.Natures) != 1 {
			t.Fatal(v)
		}
	}
	if v := check("teacher"); v.Total != 2 || len(v.Departments) != 1 || len(v.Categories) != 0 || len(v.Natures) != 0 {
		t.Fatal(v)
	}
	added := c.Import{SchoolID: "school", SourceID: "fixture-add", Revision: 1, FetchedAt: time.Now(), Objects: []c.Object{{ID: "new-course", Kind: "course", SchoolID: "school", Name: "New", DepartmentID: "empty-department", CategoryID: "empty-category", NatureID: "empty-nature"}}}
	if _, e := s.Import(ctx, p, "added", added); e != nil {
		t.Fatal(e)
	}
	if v := check("course"); v.Total != 2 || len(v.Departments) != 2 || len(v.Categories) != 2 || len(v.Natures) != 2 {
		t.Fatal("new option did not appear", v)
	}
	object, e := s.Get(ctx, "new-course")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetState(ctx, p, "new-course", "hide", c.ObjectState{Revision: object.Revision, Inactive: true, Reason: "Synthetic hide"}); e != nil {
		t.Fatal(e)
	}
	if v := check("course"); v.Total != 1 || len(v.Categories) != 1 {
		t.Fatal("hidden course stayed cached", v)
	}
	if _, e = repo.DB.Exec(`UPDATE catalog.teacher_directory SET teaching=false WHERE teacher_id IN ('teacher','teacher2')`); e != nil {
		t.Fatal(e)
	}
	if v := check("teacher"); v.Total != 0 || len(v.Departments) != 0 {
		t.Fatal("unmarked teachers stayed cached", v)
	}
	if _, e = s.ReadFilterOptions(ctx, c.Principal{}, "course", ""); !errors.Is(e, c.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if _, e = s.ReadFilterOptions(ctx, reader, "course", "other"); !errors.Is(e, c.ErrForbidden) {
		t.Fatal(e)
	}
	s.ReadAccess = &readAccess{revoked: true}
	if _, e = s.ReadFilterOptions(ctx, reader, "course", ""); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revocation cached", e)
	}
}

func TestFilterOptionsAliasAndInactiveRelations(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	reader := c.Principal{Subject: "issuer:student"}
	in.Objects = append(in.Objects, c.Object{ID: "second", Kind: "course", SchoolID: "school", Name: "Second", DepartmentID: "dept"})
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	before, e := s.ReadFilterOptions(ctx, reader, "course", "")
	if e != nil || before.Total != 2 {
		t.Fatal(before, e)
	}
	// Exercise the alias generation trigger directly, as the module's merge writes
	// this same catalog-owned mapping. The old course must count only once.
	repo := s.Repo.(*postgres.Repository)
	if _, e = repo.DB.Exec(`INSERT INTO catalog.redirects(alias_id,canonical_id,reason,created_at) VALUES('course','second','Synthetic merge',now())`); e != nil {
		t.Fatal(e)
	}
	after, e := s.ReadFilterOptions(ctx, reader, "course", "")
	if e != nil || after.Total != 1 || len(after.Categories) != 1 || after.Categories[0].Count != 1 {
		t.Fatal("alias cache/count", after, e)
	}
	if _, e = repo.DB.Exec(`UPDATE catalog.objects SET data=jsonb_set(data,'{object,inactive}','true') WHERE kind='class'`); e != nil {
		t.Fatal(e)
	}
	after, e = s.ReadFilterOptions(ctx, reader, "course", "")
	if e != nil || len(after.Categories) != 0 {
		t.Fatal("inactive relations", after, e)
	}
}

func TestFilterOptionsProductionScale(t *testing.T) {
	repo, admin := database(t)
	ctx := context.Background()
	if _, e := admin.Exec(directoryScaleSeed); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(`INSERT INTO catalog.teacher_directory(teacher_id,school_id,teaching,updated_at) SELECT id,school_id,true,clock_timestamp() FROM catalog.objects WHERE kind='teacher' AND right(id,1)<>'0'; ANALYZE catalog.teacher_directory`); e != nil {
		t.Fatal(e)
	}
	if e := postgres.Migrate(ctx, admin); e != nil {
		t.Fatal(e)
	}
	repo.RequireTeacherDirectory = true
	repo.DB.SetMaxOpenConns(3)
	s := &c.Service{Repo: repo, ReadAccess: uiScaleAccess{}}
	p := c.Principal{Subject: "synthetic-reader"}
	for _, kind := range []string{"course", "teacher"} {
		start := time.Now()
		v, e := s.ReadFilterOptions(ctx, p, kind, "")
		cold := time.Since(start)
		if e != nil || len(v.Departments) == 0 {
			t.Fatal(v, e)
		}
		timings := []time.Duration{}
		for range 30 {
			start = time.Now()
			if _, e = s.ReadFilterOptions(ctx, p, kind, ""); e != nil {
				t.Fatal(e)
			}
			timings = append(timings, time.Since(start))
		}
		sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
		p95 := timings[28]
		t.Logf("557205 rows; kind=%s cold=%s warm_p95=%s max=%s", kind, cold, p95, timings[29])
		if p95 > 300*time.Millisecond {
			t.Fatal("filter options performance regression", p95)
		}
	}
}
