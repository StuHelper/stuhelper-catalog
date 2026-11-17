package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"os"
	"slices"
	"testing"
)

func TestImportPreviewRollsBackAndRejectsChangedConfirmation(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	preview, err := s.PreviewImport(ctx, p, in)
	if err != nil || !preview.Valid || preview.PreviewToken == "" || len(preview.Changes) != len(in.Objects)+len(in.Enrollments) {
		t.Fatal(preview, err)
	}
	if _, err = s.Get(ctx, "course"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("preview wrote records", err)
	}
	in.PreviewToken = preview.PreviewToken
	if _, err = s.Import(ctx, p, "confirm", in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Import(ctx, p, "confirm", in); err != nil {
		t.Fatal("confirmation replay failed", err)
	}
	in.PreviewToken = ""
	in.Revision = 2
	plan, err := s.PreviewImport(ctx, p, in)
	if err != nil || !plan.Valid {
		t.Fatal(plan, err)
	}
	title := "Manual name"
	if _, err = s.Correct(ctx, p, "course", "edit", c.Correction{Revision: 1, Name: &title}); err != nil {
		t.Fatal(err)
	}
	in.PreviewToken = plan.PreviewToken
	if _, err = s.Import(ctx, p, "stale", in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale confirmation overwrote newer state", err)
	}
	in.PreviewToken = ""
	plan, err = s.PreviewImport(ctx, p, in)
	if err != nil || !plan.Valid {
		t.Fatal(plan, err)
	}
	in.PreviewToken = plan.PreviewToken
	if _, err = s.Import(ctx, p, "fresh", in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "course")
	if got.Name != title {
		t.Fatal("manual correction lost")
	}
}
func TestMergeKeepsOldLinksOfferingsAndEnrollmentsAndSupportsDeactivation(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	extra := c.Object{ID: "course-copy", Kind: "course", SchoolID: "school", Name: "Copy", Aliases: []string{}}
	offering := c.Object{ID: "offering-copy", Kind: "term_course", SchoolID: "school", Name: "Copy offering", CourseID: "course-copy", TermID: "term1", Aliases: []string{}}
	class := c.Object{ID: "class-copy", Kind: "class", SchoolID: "school", Name: "Copy class", TermCourseID: "offering-copy", Aliases: []string{}}
	in.Objects = append(in.Objects, extra, offering, class)
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	req := c.MergeInput{TargetID: "course-copy", SourceRevision: 1, TargetRevision: 1, Reason: "Confirmed duplicate"}
	plan, err := s.PreviewMerge(ctx, p, "course", req)
	if err != nil {
		t.Fatal(err)
	}
	req.PreviewToken = plan.PreviewToken
	if _, err = s.Merge(ctx, c.Principal{Subject: "other", CanMaintain: true, Schools: []string{"other"}}, "course", "bad", req); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	merged, err := s.Merge(ctx, p, "course", "merge", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Merge(ctx, p, "course", "merge", req); err != nil {
		t.Fatal(err)
	}
	old, err := s.Get(ctx, "course")
	if err != nil || old.CanonicalID != "course-copy" || old.Name != "Copy" {
		t.Fatal(old, err)
	}
	courses, err := s.List(ctx, c.Filter{Kind: "course", Limit: 100})
	if err != nil || len(courses.Items) != 1 {
		t.Fatal("duplicate courses", courses, err)
	}
	offerings, err := s.List(ctx, c.Filter{Kind: "term_course", CourseID: "course", Limit: 100})
	if err != nil || len(offerings.Items) != 2 {
		t.Fatal("logical offerings were not combined", offerings, err)
	}
	classes, err := s.List(ctx, c.Filter{Kind: "class", TermCourseID: "offering1", Limit: 100})
	if err != nil || len(classes.Items) != 3 {
		t.Fatal("class links lost", classes, err)
	}
	enrollments, err := s.MyEnrollments(ctx, c.Principal{Subject: "issuer:student"}, "school", "course-copy", "term1", "", 100)
	if err != nil || len(enrollments) != 1 || enrollments[0].CourseID != "course-copy" {
		t.Fatal(enrollments, err)
	}
	state, err := s.SetState(ctx, p, "course-copy", "disable", c.ObjectState{Revision: merged.Revision, Inactive: true, Reason: "No longer offered"})
	if err != nil || !state.Inactive {
		t.Fatal(state, err)
	}
	courses, err = s.List(ctx, c.Filter{Kind: "course", Limit: 100})
	if err != nil || len(courses.Items) != 0 {
		t.Fatal(courses, err)
	}
	old, err = s.Get(ctx, "course")
	if err != nil || !old.Inactive {
		t.Fatal("history disappeared", old, err)
	}
	courses, err = s.List(ctx, c.Filter{Kind: "course", Limit: 100, IncludeInactive: true})
	if err != nil || len(courses.Items) != 1 {
		t.Fatal(courses, err)
	}
}

func TestMergeRejectsFullAliasListWithoutDroppingSourceName(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	aliases := []string{}
	for i := 0; i < 50; i++ {
		aliases = append(aliases, fmt.Sprintf("Known name %d", i))
	}
	in.Objects = append(in.Objects, c.Object{ID: "target", Kind: "course", SchoolID: "school", Name: "Target course", FormerNames: aliases})
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewMerge(ctx, p, "course", c.MergeInput{TargetID: "target", SourceRevision: 1, TargetRevision: 1, Reason: "Checked duplicate"}); !errors.Is(err, c.ErrInput) {
		t.Fatal("merge would silently drop the source name", err)
	}
	original, err := s.Get(ctx, "course")
	if err != nil || original.CanonicalID != "" {
		t.Fatal("rejected merge changed source", original, err)
	}
}

func TestMaintenanceWriteUnifiedAuthAliasGuardAndOpenAPISchema(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects,
		c.Object{ID: "course-dup", Kind: "course", SchoolID: "school", Name: "Duplicate Course", DepartmentID: "dept", Aliases: []string{}},
	)
	if _, err := s.Import(ctx, p, "seed", in); err != nil {
		t.Fatal(err)
	}

	name := "Edited"
	anon := c.Principal{}
	student := c.Principal{Subject: "issuer:student", Schools: []string{"school"}}
	wrongSchool := c.Principal{Subject: "issuer:other-admin", CanMaintain: true, Schools: []string{"other-school"}}
	mergeReq := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "Duplicate", PreviewToken: "tok"}
	stateReq := c.ObjectState{Revision: 1, Inactive: true, Reason: "Hide"}

	// Upfront authorization without object ID enumeration.
	for _, tc := range []struct {
		who  c.Principal
		want error
	}{
		{anon, c.ErrUnauthenticated},
		{student, c.ErrForbidden},
	} {
		if _, err := s.Correct(ctx, tc.who, "nonexistent-id", "k1", c.Correction{Revision: 1, Name: &name}); !errors.Is(err, tc.want) {
			t.Fatalf("Correct auth (%+v): got %v want %v", tc.who, err, tc.want)
		}
		if _, err := s.PreviewMerge(ctx, tc.who, "nonexistent-id", mergeReq); !errors.Is(err, tc.want) {
			t.Fatalf("PreviewMerge auth (%+v): got %v want %v", tc.who, err, tc.want)
		}
		if _, err := s.Merge(ctx, tc.who, "nonexistent-id", "k1", mergeReq); !errors.Is(err, tc.want) {
			t.Fatalf("Merge auth (%+v): got %v want %v", tc.who, err, tc.want)
		}
		if _, err := s.SetState(ctx, tc.who, "nonexistent-id", "k1", stateReq); !errors.Is(err, tc.want) {
			t.Fatalf("SetState auth (%+v): got %v want %v", tc.who, err, tc.want)
		}
		if _, err := s.Delete(ctx, tc.who, "nonexistent-id", "k1", stateReq); !errors.Is(err, tc.want) {
			t.Fatalf("Delete auth (%+v): got %v want %v", tc.who, err, tc.want)
		}
	}

	// School-scope check.
	if _, err := s.Correct(ctx, wrongSchool, "course", "k2", c.Correction{Revision: 1, Name: &name}); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("Correct cross-school: %v", err)
	}
	if _, err := s.PreviewMerge(ctx, wrongSchool, "course-dup", mergeReq); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("PreviewMerge cross-school: %v", err)
	}

	// Merge course-dup -> course, verify idempotent replay, then verify ALL maintenance writes reject the merged alias with 409 Conflict.
	plan, err := s.PreviewMerge(ctx, p, "course-dup", c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "Confirmed duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	execMerge := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "Confirmed duplicate", PreviewToken: plan.PreviewToken}
	merged, err := s.Merge(ctx, p, "course-dup", "merge-dup", execMerge)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Merge(ctx, p, "course-dup", "merge-dup", execMerge)
	if err != nil || replayed.ID != merged.ID || replayed.Revision != merged.Revision {
		t.Fatalf("Merge replay after canonical redirect failed: %+v %v", replayed, err)
	}
	if _, err := s.Correct(ctx, p, "course-dup", "edit-alias", c.Correction{Revision: 2, Name: &name}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("Correct on merged alias must fail with ErrConflict, got: %v", err)
	}
	if _, err := s.PreviewMerge(ctx, p, "course-dup", c.MergeInput{TargetID: "course", SourceRevision: 2, TargetRevision: merged.Revision, Reason: "Again"}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("PreviewMerge on merged alias must fail with ErrConflict, got: %v", err)
	}
	if _, err := s.Merge(ctx, p, "course-dup", "merge-dup-new-key", execMerge); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("Merge with new key on merged alias must fail with ErrConflict, got: %v", err)
	}
	if _, err := s.SetState(ctx, p, "course-dup", "state-alias", c.ObjectState{Revision: 2, Inactive: true, Reason: "Hide alias"}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("SetState on merged alias must fail with ErrConflict, got: %v", err)
	}

	// Verify OpenAPI NullableObject stays in lockstep with Object and both Summary/PublicSummary reference it.
	raw, err := os.ReadFile("contracts/catalog.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Nullable   bool                       `json:"nullable"`
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	objSchema, okObj := spec.Components.Schemas["Object"]
	nullObjSchema, okNull := spec.Components.Schemas["NullableObject"]
	if !okObj || !okNull || !nullObjSchema.Nullable {
		t.Fatalf("missing Object or NullableObject schema")
	}
	objKeys, nullKeys := make([]string, 0, len(objSchema.Properties)), make([]string, 0, len(nullObjSchema.Properties))
	for k := range objSchema.Properties {
		objKeys = append(objKeys, k)
	}
	for k := range nullObjSchema.Properties {
		nullKeys = append(nullKeys, k)
	}
	slices.Sort(objKeys)
	slices.Sort(nullKeys)
	if !slices.Equal(objKeys, nullKeys) || !slices.Equal(objSchema.Required, nullObjSchema.Required) {
		t.Fatalf("NullableObject drifted from Object: obj=%v null=%v", objKeys, nullKeys)
	}
	for _, summaryName := range []string{"Summary", "PublicSummary"} {
		var deptRef struct {
			Ref string `json:"$ref"`
		}
		if err := json.Unmarshal(spec.Components.Schemas[summaryName].Properties["department"], &deptRef); err != nil || deptRef.Ref != "#/components/schemas/NullableObject" {
			t.Fatalf("%s.department must $ref NullableObject, got: %s (%v)", summaryName, string(spec.Components.Schemas[summaryName].Properties["department"]), err)
		}
	}
}
