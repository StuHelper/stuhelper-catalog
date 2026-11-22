package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestVerifiedTeacherHomepagePublicationCreatesIndependentDirectoryEvidence(t *testing.T) {
	s, admin, db, _ := projectionFixture(t)
	ctx := context.Background()
	reader := c.Principal{Subject: "issuer:student"}

	const teacher = "independent"
	const other = "unapproved"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := s.Import(ctx, admin, "independent-teacher-seed", c.Import{
		SchoolID: "school", SourceID: "source", Revision: 2, FetchedAt: now,
		Objects: []c.Object{
			{ID: teacher, Kind: "teacher", SchoolID: "school", Name: "Verified homepage teacher", DepartmentID: "dept", Aliases: []string{}},
			{ID: other, Kind: "teacher", SchoolID: "school", Name: "Other directory teacher", DepartmentID: "dept", Aliases: []string{}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	checkVisible := func(want bool) {
		t.Helper()
		o, err := s.Read(ctx, reader, teacher)
		if want && (err != nil || o.ID != teacher) || !want && !errors.Is(err, c.ErrNotFound) {
			t.Fatalf("detail visible=%v: %+v, %v", want, o, err)
		}
		page, err := s.ReadList(ctx, reader, c.Filter{Kind: "teacher", SchoolID: "school", Limit: 50})
		if err != nil || slices.ContainsFunc(page.Items, func(o c.Object) bool { return o.ID == teacher }) != want {
			t.Fatalf("directory visible=%v: %+v, %v", want, page, err)
		}
		ranked, err := s.ReadDirectory(ctx, reader, c.DirectoryQuery{
			Filter: c.Filter{Kind: "teacher", SchoolID: "school", Limit: 50},
		})
		if err != nil || slices.ContainsFunc(ranked.Items, func(item c.DirectoryItem) bool {
			return item.Object.ID == teacher
		}) != want {
			t.Fatalf("ranked directory visible=%v: %+v, %v", want, ranked, err)
		}
		filters, err := s.ReadFilterOptions(ctx, reader, "teacher", "school")
		if err != nil || filters.Total != int64(len(page.Items)) {
			t.Fatalf("teacher filters disagree with current directory: %+v, %v", filters, err)
		}
		search, err := s.DiscoveryCurrent(ctx, reader, []string{teacher, other})
		if err != nil || want && (len(search) != 1 || !search[0].Visible || search[0].Target.ID != teacher) ||
			!want && len(search) != 0 {
			t.Fatalf("search visible=%v: %+v, %v", want, search, err)
		}
	}
	counts := func() c.TeacherHomepageCounts {
		t.Helper()
		out, err := s.ReadTeacherHomepageCounts(ctx, admin, "school")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	grant := func(school, id string, at time.Time, after error) error {
		return s.Repo.Transaction(ctx, func(tx c.Transaction) error {
			if err := tx.Lock(school); err != nil {
				return err
			}
			if err := tx.(c.TeacherHomepagePublicationTransaction).GrantVerifiedTeacherHomepage(school, id, at); err != nil {
				return err
			}
			return after
		})
	}
	publish := func() {
		t.Helper()
		report, err := s.FlushPendingProjection(ctx, admin, "school")
		if err != nil || report == nil {
			t.Fatalf("publish pending directory: %+v, %v", report, err)
		}
	}

	checkVisible(false)
	baseline := counts()
	if _, err := s.ReadTeacherHomepageCounts(ctx, reader, "school"); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("non-admin counts: %v", err)
	}
	if _, err := s.ReadTeacherHomepageCounts(ctx, admin, "other-school"); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("wrong-school counts: %v", err)
	}
	if err := grant("other-school", teacher, now, nil); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("wrong-school grant: %v", err)
	}
	rollback := errors.New("synthetic failure writing approval audit")
	if err := grant("school", teacher, now, rollback); !errors.Is(err, rollback) || counts() != baseline {
		t.Fatalf("rollback leaked eligibility: %v", err)
	}
	if err := grant("school", teacher, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := grant("school", teacher, now.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	var approvedAt time.Time
	if err := db.QueryRow(`SELECT verified_homepage_at FROM catalog.teacher_directory WHERE teacher_id=$1 AND verified_homepage AND NOT teaching AND NOT official`, teacher).Scan(&approvedAt); err != nil || !approvedAt.Equal(now) {
		t.Fatalf("independent approval provenance: %v %v", approvedAt, err)
	}
	wantCounts := baseline
	wantCounts.VerifiedHomepage++
	wantCounts.Eligible++
	if got := counts(); got != wantCounts {
		t.Fatalf("approval counted as the wrong evidence: got %+v, want %+v", got, wantCounts)
	}

	checkVisible(false)
	publish()
	checkVisible(true)
	var discoverEvents int
	err := db.QueryRow(`SELECT count(*) FROM catalog.outbox WHERE data->>'type'='catalog.discovery.v1'
 AND data->>'subject'=$1 AND (data->'data'->>'visible')::boolean`, teacher).Scan(&discoverEvents)
	if err != nil || discoverEvents != 1 {
		t.Fatalf("approved teacher discovery events=%d err=%v", discoverEvents, err)
	}

	revision := int64(0)
	for _, visibility := range []string{"hidden", "stopped"} {
		receipt, err := s.SetTeacherHomepageControls(ctx, admin, "school", teacher, "publication-"+visibility, c.TeacherHomepageControlsInput{
			ExpectedRevision: &revision, PageVisibility: visibility, Rules: []c.TeacherHomepageFieldRule{},
		})
		if err != nil || receipt.Revision <= revision {
			t.Fatalf("set visibility %s: %+v, %v", visibility, receipt, err)
		}
		current, err := s.ReadTeacherHomepageControls(ctx, admin, "school", teacher)
		if err != nil || !current.Controls.Independent || current.Controls.PageVisibility != visibility {
			t.Fatalf("visibility %s lost approval provenance: %+v, %v", visibility, current, err)
		}
		revision = receipt.Revision
		checkVisible(true)
		if got := counts(); got != wantCounts {
			t.Fatalf("profile visibility changed directory evidence: %+v", got)
		}
	}
	candidates, err := s.TeacherCandidates(ctx, admin, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportTeacherSites(ctx, admin, c.TeacherSiteSnapshot{
		SchoolID: "school", SourceID: "official", Revision: 2, ObservedAt: now, CandidateFingerprint: c.TeacherCandidateFingerprint(candidates),
		Matches: []c.TeacherSiteMatch{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshTeachingDirectory(ctx, admin, "school"); err != nil {
		t.Fatal(err)
	}
	checkVisible(true)
	if got := counts(); got.VerifiedHomepage != 1 || got.Official != 0 {
		t.Fatalf("source refresh changed approval provenance: %+v", got)
	}

	s.AsyncMaintenanceRebuild = true
	current, err := s.AdminRead(ctx, admin, teacher)
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := s.SetState(ctx, admin, teacher, "independent-inactive", c.ObjectState{
		Revision: current.Revision, Inactive: true, Reason: "Synthetic directory maintenance",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkVisible(false)
	if got := counts(); got.VerifiedHomepage != 0 {
		t.Fatalf("inactive teacher still counted: %+v", got)
	}
	if err := db.QueryRow(`SELECT count(*) FROM catalog.teacher_homepage_review_state WHERE school_id='school'`).Scan(&discoverEvents); err != nil ||
		discoverEvents != 0 {
		t.Fatalf("independent revocation unexpectedly activated v2 website import state: count=%d err=%v", discoverEvents, err)
	}
	publish()
	if err := grant("school", teacher, now, nil); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("inactive grant: %v", err)
	}
	if _, err := s.SetState(ctx, admin, teacher, "independent-active", c.ObjectState{
		Revision: inactive.Revision, Inactive: false, Reason: "Synthetic directory restoration",
	}); err != nil {
		t.Fatal(err)
	}
	publish()
	checkVisible(true)

	source, err := s.AdminRead(ctx, admin, teacher)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.AdminRead(ctx, admin, other)
	if err != nil {
		t.Fatal(err)
	}
	in := c.MergeInput{
		TargetID: other, SourceRevision: source.Revision, TargetRevision: target.Revision,
		Reason: "Synthetic canonical merge",
	}
	plan, err := s.PreviewMerge(ctx, admin, teacher, in)
	if err != nil {
		t.Fatal(err)
	}
	in.PreviewToken = plan.PreviewToken
	if _, err := s.Merge(ctx, admin, teacher, "independent-merge", in); err != nil {
		t.Fatal(err)
	}
	checkVisible(false)
	publish()
	checkVisible(false)
	if _, err := s.Read(ctx, reader, other); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("merge transferred approval to another teacher: %v", err)
	}
	if err := grant("school", teacher, now, nil); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("alias grant: %v", err)
	}

	// The preceding generation may already expose this teacher through teaching
	// when the new approval and subsequent deactivation arrive before a build.
	const taught = "teacher"
	if err := grant("school", taught, now, nil); err != nil {
		t.Fatal(err)
	}
	var oldTeaching, oldVerified bool
	err = db.QueryRow(`SELECT p.teaching,p.verified_homepage
 FROM catalog.projection_objects p JOIN catalog.projection_heads h ON h.version=p.version WHERE p.id=$1`, taught).
		Scan(&oldTeaching, &oldVerified)
	if err != nil || !oldTeaching || oldVerified {
		t.Fatalf("preceding projection should only have teaching evidence: %v %v %v", oldTeaching, oldVerified, err)
	}
	taughtObject, err := s.AdminRead(ctx, admin, taught)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetState(ctx, admin, taught, "recently-approved-inactive", c.ObjectState{
		Revision: taughtObject.Revision, Inactive: true, Reason: "Synthetic directory maintenance",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, reader, taught); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("teaching in the preceding projection bypassed deactivation: %v", err)
	}
	if docs, err := s.DiscoveryCurrent(ctx, reader, []string{taught}); err != nil || len(docs) != 0 {
		t.Fatalf("deactivation left a current discovery result: %+v %v", docs, err)
	}
}
