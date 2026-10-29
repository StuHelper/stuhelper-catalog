package catalog_test

import (
	"context"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
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
	in.Objects = append(in.Objects, c.Object{ID: "target", Kind: "course", SchoolID: "school", Name: "Target course", Aliases: aliases})
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
