package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"os"
	"slices"
	"sync/atomic"
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

type countingRepo struct {
	c.Repository
	txCount atomic.Int64
}

func (r *countingRepo) Transaction(ctx context.Context, run func(c.Transaction) error) error {
	r.txCount.Add(1)
	return r.Repository.Transaction(ctx, run)
}

func changeByID(changes []c.ImportChange, id string) (c.ImportChange, bool) {
	for _, ch := range changes {
		if ch.ID == id {
			return ch, true
		}
	}
	return c.ImportChange{}, false
}

func TestImportPreviewSingleTransactionManualFieldAndSameRevisionConflict(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	if _, err := s.Import(ctx, p, "seed-preview-a12", in); err != nil {
		t.Fatal(err)
	}
	manualTitle := "Manually Curated Course"
	if _, err := s.Correct(ctx, p, "course", "manual-title", c.Correction{Revision: 1, Name: &manualTitle}); err != nil {
		t.Fatal(err)
	}
	// Deactivate then restore teacher2 so Manual retains inactive_reason / inactive=false.
	hidden, err := s.SetState(ctx, p, "teacher2", "hide-t2", c.ObjectState{Revision: 1, Inactive: true, Reason: "temp check"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetState(ctx, p, "teacher2", "restore-t2", c.ObjectState{Revision: hidden.Revision, Inactive: false, Reason: "restored"}); err != nil {
		t.Fatal(err)
	}

	counted := &countingRepo{Repository: s.Repo}
	s.Repo = counted

	// 1. Identical source payload at revision 2 runs in a single rollback transaction and marks unchanged.
	same := in
	same.Objects = slices.Clone(in.Objects)
	same.Enrollments = slices.Clone(in.Enrollments)
	same.Revision = 2
	counted.txCount.Store(0)
	previewSame, err := s.PreviewImport(ctx, p, same)
	if err != nil || !previewSame.Valid {
		t.Fatalf("PreviewImport identical rev 2 failed: %+v err=%v", previewSame, err)
	}
	if gotTx := counted.txCount.Load(); gotTx != 1 {
		t.Fatalf("PreviewImport executed %d transactions, want 1 atomic rollback transaction", gotTx)
	}
	for _, id := range []string{"course", "teacher2", "enrollment"} {
		ch, ok := changeByID(previewSame.Changes, id)
		if !ok || ch.State != "unchanged" {
			t.Fatalf("expected %s state=unchanged on identical source payload, got %+v", id, ch)
		}
	}

	// 2. Updating a non-manual field (credits) on manually-named course reports "updated", not "preserved".
	updateNonManual := in
	updateNonManual.Objects = slices.Clone(in.Objects)
	updateNonManual.Revision = 2
	newCredits := 4.5
	for i := range updateNonManual.Objects {
		if updateNonManual.Objects[i].ID == "course" {
			updateNonManual.Objects[i].Credits = &newCredits
		}
	}
	previewUpdated, err := s.PreviewImport(ctx, p, updateNonManual)
	if err != nil || !previewUpdated.Valid {
		t.Fatalf("PreviewImport non-manual field update failed: %+v err=%v", previewUpdated, err)
	}
	if ch, ok := changeByID(previewUpdated.Changes, "course"); !ok || ch.State != "updated" || !slices.Equal(ch.Fields, []string{"credits"}) {
		t.Fatalf("expected course state=updated fields=[credits], got %+v", ch)
	}

	// 3. Updating the manually-overridden field (name) in incoming source reports "preserved".
	updateManual := in
	updateManual.Objects = slices.Clone(in.Objects)
	updateManual.Revision = 2
	for i := range updateManual.Objects {
		if updateManual.Objects[i].ID == "course" {
			updateManual.Objects[i].Name = "Upstream Renamed Course"
		}
	}
	previewPreserved, err := s.PreviewImport(ctx, p, updateManual)
	if err != nil || !previewPreserved.Valid {
		t.Fatalf("PreviewImport manual field override failed: %+v err=%v", previewPreserved, err)
	}
	if ch, ok := changeByID(previewPreserved.Changes, "course"); !ok || ch.State != "preserved" || !slices.Equal(ch.Fields, []string{"name"}) {
		t.Fatalf("expected course state=preserved fields=[name], got %+v", ch)
	}

	// 4. Same source revision (Revision == 1) with modified content reports state="conflict" on modified items.
	sameRevConflict := in
	sameRevConflict.Objects = slices.Clone(in.Objects)
	sameRevConflict.Enrollments = slices.Clone(in.Enrollments)
	sameRevConflict.Revision = 1
	for i := range sameRevConflict.Objects {
		if sameRevConflict.Objects[i].ID == "dept" {
			sameRevConflict.Objects[i].Name = "Conflicting Same Revision Dept"
		}
		if sameRevConflict.Objects[i].ID == "course" {
			sameRevConflict.Objects[i].Credits = &newCredits
		}
	}
	sameRevConflict.Enrollments[0].ClassID = "class2"
	sameRevConflict.Enrollments[0].TeacherIDs = []string{"teacher2"}
	previewConflict, err := s.PreviewImport(ctx, p, sameRevConflict)
	if err != nil || previewConflict.Valid || previewConflict.Code != "conflict" {
		t.Fatalf("expected top-level conflict on same-revision diff, got %+v err=%v", previewConflict, err)
	}
	for _, id := range []string{"dept", "course", "enrollment"} {
		ch, ok := changeByID(previewConflict.Changes, id)
		if !ok || ch.State != "conflict" {
			t.Fatalf("expected %s state=conflict on same-revision diff, got %+v", id, ch)
		}
	}
}

func TestMaintenancePreAuthAndRejectMergedSourceCorrection(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects,
		c.Object{ID: "course-old", Kind: "course", SchoolID: "school", Name: "Old Course", Aliases: []string{}},
		c.Object{ID: "sched-1", Kind: "schedule", SchoolID: "school", ClassID: "class1", Name: "Sched", Aliases: []string{}},
	)
	if _, err := s.Import(ctx, admin, "seed", in); err != nil {
		t.Fatal(err)
	}

	// A-05: Unauthenticated and non-maintainer callers must be rejected with 401/403
	// BEFORE probing object existence or returning 400 on PreviewMerge.
	student := c.Principal{Subject: "issuer:student"}
	anon := c.Principal{}
	name := "Probe"
	corr := c.Correction{Revision: 1, Name: &name}
	mergeIn := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "probe", PreviewToken: "tok"}
	stateIn := c.ObjectState{Revision: 1, Inactive: true, Reason: "probe"}
	tt := timetable()

	for _, targetID := range []string{"class1", "nonexistent-secret-id"} {
		if _, err := s.Correct(ctx, student, targetID, "k1", corr); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("student Correct(%s) got %v, want ErrForbidden", targetID, err)
		}
		if _, err := s.Merge(ctx, student, targetID, "k2", mergeIn); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("student Merge(%s) got %v, want ErrForbidden", targetID, err)
		}
		if _, err := s.SetState(ctx, student, targetID, "k3", stateIn); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("student SetState(%s) got %v, want ErrForbidden", targetID, err)
		}
		if _, err := s.PreviewMerge(ctx, student, targetID, mergeIn); !errors.Is(err, c.ErrForbidden) {
			t.Fatalf("student PreviewMerge(%s) got %v, want ErrForbidden", targetID, err)
		}
		if _, err := s.Correct(ctx, anon, targetID, "k1", corr); !errors.Is(err, c.ErrUnauthenticated) {
			t.Fatalf("anon Correct(%s) got %v, want ErrUnauthenticated", targetID, err)
		}
		if _, err := s.PreviewMerge(ctx, anon, targetID, mergeIn); !errors.Is(err, c.ErrUnauthenticated) {
			t.Fatalf("anon PreviewMerge(%s) got %v, want ErrUnauthenticated", targetID, err)
		}
	}
	if _, err := s.GetTimetable(ctx, student, "school", "nonexistent-term"); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("student GetTimetable got %v, want ErrForbidden", err)
	}
	if _, err := s.GetTimetable(ctx, anon, "school", "term1"); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatalf("anon GetTimetable got %v, want ErrUnauthenticated", err)
	}
	if _, err := s.PutTimetable(ctx, anon, tt); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatalf("anon PutTimetable got %v, want ErrUnauthenticated", err)
	}

	// A-08(1): Correct on a merged source alias must be rejected with 409 ErrConflict and not mutate DB.
	mReq := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 1, Reason: "duplicate"}
	plan, err := s.PreviewMerge(ctx, admin, "course-old", mReq)
	if err != nil {
		t.Fatal(err)
	}
	mReq.PreviewToken = plan.PreviewToken
	if _, err := s.Merge(ctx, admin, "course-old", "merge-old", mReq); err != nil {
		t.Fatal(err)
	}
	oldAdmin, err := s.AdminRead(ctx, admin, "course-old")
	if err != nil {
		t.Fatal(err)
	}
	repo := s.Repo.(*postgres.Repository)
	var histBefore int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM catalog.history WHERE id='course-old'`).Scan(&histBefore); err != nil {
		t.Fatal(err)
	}
	forgedName := "Forged Name On Alias"
	// Note: Merge incremented course-old's raw revision to 2.
	if _, err := s.Correct(ctx, admin, "course-old", "correct-alias", c.Correction{Revision: 2, Name: &forgedName}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("Correct on merged source alias returned %v (oldAdmin=%+v), want ErrConflict", err, oldAdmin)
	}
	var histAfter int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM catalog.history WHERE id='course-old'`).Scan(&histAfter); err != nil || histAfter != histBefore {
		t.Fatalf("merged alias history mutated: before=%d after=%d err=%v", histBefore, histAfter, err)
	}

	// A-08(2): Correct must reject fields inapplicable to the object's Kind.
	bio := "Invalid bio"
	dept := "dept"
	code := "CODE1"
	if _, err := s.Correct(ctx, admin, "sched-1", "bad-bio", c.Correction{Revision: 1, Biography: &bio}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("Biography on schedule got %v, want ErrInput", err)
	}
	if _, err := s.Correct(ctx, admin, "sched-1", "bad-dept", c.Correction{Revision: 1, DepartmentID: &dept}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("DepartmentID on schedule got %v, want ErrInput", err)
	}
	if _, err := s.Correct(ctx, admin, "teacher", "bad-code", c.Correction{Revision: 1, Code: &code}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("Code on teacher got %v, want ErrInput", err)
	}
}
