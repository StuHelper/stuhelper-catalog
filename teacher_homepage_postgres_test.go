package catalog_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func teacherHomepagePostgresSnapshot(t *testing.T, s *c.Service, p c.Principal) c.TeacherSiteSnapshot {
	t.Helper()
	candidates, err := s.TeacherCandidates(context.Background(), p, "school", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	photo := image.NewRGBA(image.Rect(0, 0, 2, 3))
	photo.Set(0, 0, color.RGBA{R: 190, A: 255})
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, photo); err != nil {
		t.Fatal(err)
	}
	portrait := imageData.Bytes()
	sum := sha256.Sum256(portrait)
	photoID := hex.EncodeToString(sum[:])
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)

	site := func(raw, name, unit, reason string, candidateIDs ...string) c.TeacherSite {
		return c.TeacherSite{
			SiteID: c.TeacherSiteID("school", raw), URL: "https://shi.buaa.edu.cn/" + raw + "/en/index.htm",
			Name: name, Unit: unit, Reason: reason, CandidateTeacherIDs: candidateIDs, CandidateCount: int64(len(candidateIDs)),
			Profile: c.TeacherHomepage{
				Name: name, Email: raw + "@example.org", Phone: "555-0100", OfficeLocation: "Synthetic lab", HireDate: "2023-08",
				Biography: "Synthetic website biography", Research: "Synthetic research", Discipline: "Synthetic discipline",
				Fields:   []c.TeacherHomepageField{{Label: "学术兼职", Value: "A field from the website"}},
				Sections: []c.TeacherHomepageSection{{Title: "Publications", Text: "Synthetic publication"}},
			},
		}
	}
	automatic := site("auto", "Official", "Department", "", "official")
	manual := site("manual", "Legacy", "External Unit 100%_", "unit_mismatch", "legacy")
	rejected := site("reject", "Teacher", "Another Department", "unit_mismatch", "teacher")
	for _, row := range []*c.TeacherSite{&automatic, &manual} {
		row.Photo = &c.TeacherHomepageImage{ID: photoID, MediaType: "image/png", Width: 2, Height: 3, Bytes: portrait}
	}
	return c.TeacherSiteSnapshot{
		SchoolID: "school", SourceID: "official", Version: 2, CandidateFingerprint: c.TeacherCandidateFingerprint(candidates),
		ObservedAt: now, Revision: now.UnixMicro(),
		Matches: []c.TeacherSiteMatch{{TeacherID: "official", Name: "Official", DepartmentID: "dept", URL: automatic.URL}},
		Sites:   []c.TeacherSite{automatic, manual, rejected},
		Teachers: []c.TeacherSiteTeacher{
			{TeacherID: "official", CandidateCount: 1, CandidateSiteIDs: []string{automatic.SiteID}},
			{TeacherID: "legacy", Reason: "unit_mismatch", CandidateCount: 1, CandidateSiteIDs: []string{manual.SiteID}},
			{TeacherID: "teacher", Reason: "unit_mismatch", CandidateCount: 1, CandidateSiteIDs: []string{rejected.SiteID}},
		},
	}
}

func publishTeacherHomepagePostgresSnapshot(t *testing.T, s *c.Service, p c.Principal, key string, in c.TeacherSiteSnapshot) c.TeacherRefreshRun {
	t.Helper()
	ctx := context.Background()
	run, err := s.QueueTeacherRefresh(ctx, p, in.SchoolID, "full", "manual", key)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimTeacherRefresh(ctx, p, in.SchoolID); err != nil || claim == nil || claim.ID != run.ID {
		t.Fatalf("claim refresh: %+v %v", claim, err)
	}
	requests, count := int64(7), int64(len(in.Sites))
	out, err := s.CompleteTeacherRefresh(ctx, p, run.ID, c.TeacherRefreshCompletion{
		Snapshot: &in, Requests: &requests, SiteCount: &count,
	})
	if err != nil || out.Status != "succeeded" || out.Counts == nil {
		t.Fatalf("complete refresh: %+v %v", out, err)
	}
	return out
}

func teacherHomepageDecision(action, teacherID string, expected int64) c.TeacherSiteDecisionInput {
	return c.TeacherSiteDecisionInput{Action: action, TeacherID: teacherID, ExpectedRevision: &expected}
}

func TestTeacherHomepagePostgresProjectionAndAdmission(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	p.CanRefreshTeacherSites = true
	ctx := context.Background()
	official, err := s.AdminRead(ctx, p, "official")
	if err != nil {
		t.Fatal(err)
	}
	directoryTitle := "Directory Professor"
	if _, err := s.Correct(ctx, p, "official", "seed-title", c.Correction{Revision: official.Revision, Biography: &directoryTitle}); err != nil {
		t.Fatal(err)
	}
	in := teacherHomepagePostgresSnapshot(t, s, p)
	if counts := publishTeacherHomepagePostgresSnapshot(t, s, p, "initial", in).Counts; counts.Official != 1 || counts.Eligible != 2 {
		t.Fatalf("initial published projection: %+v", counts)
	}

	teacher, err := s.ReadWithTeacherHomepage(ctx, p, "official")
	if err != nil || teacher.TeacherHomepage == nil || teacher.Biography != directoryTitle || teacher.DepartmentID != "dept" {
		t.Fatalf("catalog facts or profile under current projection: %+v %v", teacher, err)
	}
	profile := teacher.TeacherHomepage
	if profile.Email != "" || profile.Phone != "" ||
		profile.OfficeLocation != "" || profile.HireDate != "2023-08" ||
		len(profile.Sections) != 1 || profile.Sections[0].Text != "Synthetic publication" ||
		len(profile.Fields) != 1 || profile.Fields[0].Value != "A field from the website" ||
		profile.Photo == nil || profile.Photo.Width != 2 || profile.Photo.Height != 3 {
		t.Fatalf("matched homepage payload: %+v", profile)
	}
	summary, err := s.ReadSummaryWithTeacherHomepage(ctx, p, "official")
	if err != nil || summary.Object.TeacherHomepage == nil || summary.Object.TeacherHomepage.Photo == nil ||
		summary.Object.TeacherHomepage.Photo.URL != profile.Photo.URL || summary.Object.Biography != directoryTitle ||
		summary.Department == nil || summary.Department.ID != "dept" {
		t.Fatalf("homepage lost from learner summary: %+v %v", summary, err)
	}
	page, err := s.ReadList(ctx, p, c.Filter{Kind: "teacher", Limit: 20})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("teacher list: %+v %v", page, err)
	}
	for _, item := range page.Items {
		if item.TeacherHomepage != nil {
			t.Fatalf("homepage leaked to directory: %+v", item)
		}
	}
	noHomepage, err := s.ReadWithTeacherHomepage(ctx, p, "teacher")
	if err != nil || noHomepage.TeacherHomepage != nil {
		t.Fatalf("teaching-only profile: %+v %v", noHomepage, err)
	}
	photoID := in.Sites[0].Photo.ID
	photo, err := s.ReadTeacherHomepagePhoto(ctx, p, "official", photoID)
	if err != nil || photo.MediaType != "image/png" || !bytes.Equal(photo.Bytes, in.Sites[0].Photo.Bytes) {
		t.Fatalf("portrait stored incorrectly: content type=%s size=%d error=%v", photo.MediaType, len(photo.Bytes), err)
	}
}

func TestTeacherHomepagePostgresReviewPaginationAndScope(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	p.CanReadTeacherSites, p.CanRefreshTeacherSites = true, true
	ctx := context.Background()
	q := c.TeacherReviewQuery{SchoolID: "school", Limit: 1}
	empty, err := s.ReadTeacherSiteReviews(ctx, p, q)
	if err != nil || empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("review before first v2 snapshot: %+v %v", empty, err)
	}
	in := teacherHomepagePostgresSnapshot(t, s, p)
	offlineID := c.TeacherSiteID("school", "offline")
	in.Sites = append(in.Sites, c.TeacherSite{
		SiteID: offlineID, Name: "Synthetic Offline Site", URL: "https://shi.buaa.edu.cn/offline/en/index.htm", Reason: "site_unavailable",
	})
	publishTeacherHomepagePostgresSnapshot(t, s, p, "initial", in)

	offline, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Query: "Offline", Limit: 10})
	if err != nil || offline.Total != 1 || len(offline.Items) != 1 || offline.Items[0].SiteID != offlineID ||
		offline.Items[0].Present || offline.Items[0].Reason != "site_unavailable" {
		t.Fatalf("unavailable detail is pending for review: %+v %v", offline, err)
	}
	legacy, err := s.ReadUnmatchedTeacherSites(ctx, p, "school", "", "", 10)
	if err != nil || legacy.Total != 2 || len(legacy.Items) != 2 || legacy.ReasonCounts["unit_mismatch"] != 2 {
		t.Fatalf("v2 snapshot did not preserve legacy unmatched response: %+v %v", legacy, err)
	}

	first, err := s.ReadTeacherSiteReviews(ctx, p, q)
	if err != nil || first.Total != 3 || len(first.Items) != 1 || first.Next == "" {
		t.Fatalf("site review first page: %+v %v", first, err)
	}
	q.After = first.Next
	tailQuery := q
	tailQuery.Limit = 2
	last, err := s.ReadTeacherSiteReviews(ctx, p, tailQuery)
	if err != nil || last.Total != 3 || len(last.Items) != 2 || last.Next != "" ||
		last.Items[0].SiteID <= first.Items[0].SiteID || last.Items[1].SiteID <= last.Items[0].SiteID {
		t.Fatalf("site review last page: %+v %v", last, err)
	}
	filtered := c.TeacherReviewQuery{SchoolID: "school", Query: "100%_", Limit: 10}
	sites, err := s.ReadTeacherSiteReviews(ctx, p, filtered)
	if err != nil || sites.Total != 1 || len(sites.Items) != 1 {
		t.Fatalf("literal search: %+v %v", sites, err)
	}
	row := sites.Items[0]
	if row.SiteID != in.Sites[1].SiteID || row.Reason != "unit_mismatch" || !row.Present || row.CandidateCount != 1 ||
		len(row.Candidates) != 1 || row.Candidates[0].TeacherID != "legacy" ||
		row.Candidates[0].DepartmentID != "dept" || row.Decision != nil || row.DecisionRevision != 0 {
		t.Fatalf("site candidate: %+v", row)
	}
	raw, err := json.Marshal(row)
	if err != nil || strings.Contains(string(raw), "shi.buaa.edu.cn") || strings.Contains(string(raw), "@example.org") {
		t.Fatalf("site review summary contains private payload: %s %v", raw, err)
	}

	teachers, err := s.ReadTeacherReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Limit: 1})
	if err != nil || teachers.Total != 2 || len(teachers.Items) != 1 || teachers.Next == "" {
		t.Fatalf("teacher review first page: %+v %v", teachers, err)
	}
	teacher := teachers.Items[0]
	if teacher.TeacherID != "legacy" || teacher.Reason != "unit_mismatch" || teacher.CandidateCount != 1 ||
		len(teacher.Candidates) != 1 || teacher.Candidates[0].SiteID != row.SiteID || teacher.Candidates[0].Unit != row.Unit {
		t.Fatalf("directory candidate: %+v", teacher)
	}
	if _, err := s.ReadTeacherReviews(ctx, p, q); !errors.Is(err, c.ErrInput) {
		t.Fatalf("site cursor accepted by teacher queue: %v", err)
	}
	if _, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Query: "changed", Limit: 1, After: first.Next}); !errors.Is(err, c.ErrInput) {
		t.Fatalf("cursor accepted for another query: %v", err)
	}

	readOnly := p
	readOnly.CanMaintain = false
	if _, err := s.ReadTeacherSiteReviews(ctx, readOnly, filtered); err != nil {
		t.Fatalf("read permission without write permission failed: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, readOnly, "school", row.SiteID, "denied", teacherHomepageDecision("bind", "legacy", 0)); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("review read could write: %v", err)
	}
	revoked := p
	revoked.CanReadTeacherSites = false
	if _, err := s.ReadTeacherSiteReviews(ctx, revoked, filtered); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("revoked review access: %v", err)
	}
	other := p
	other.Schools = []string{"other"}
	if _, err := s.ReadTeacherReviews(ctx, other, c.TeacherReviewQuery{SchoolID: "school", Limit: 1}); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("other school read: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, other, "school", row.SiteID, "cross-school", teacherHomepageDecision("bind", "legacy", 0)); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("other school write: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", row.SiteID, "bind-for-cursor", teacherHomepageDecision("bind", "legacy", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadTeacherSiteReviews(ctx, p, q); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("site cursor survived decision: %v", err)
	}
	if _, err := s.ReadTeacherReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Limit: 1, After: teachers.Next}); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("teacher cursor survived decision: %v", err)
	}
}

func TestTeacherHomepagePostgresDecisionAtomicity(t *testing.T) {
	s, p, migration, _ := projectionFixture(t)
	p.CanReadTeacherSites, p.CanRefreshTeacherSites = true, true
	ctx := context.Background()
	in := teacherHomepagePostgresSnapshot(t, s, p)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "initial", in)
	manualSite, rejectSite := in.Sites[1], in.Sites[2]
	bind := teacherHomepageDecision("bind", "legacy", 0)
	const key = "same-bind"

	_, err := migration.Exec(`CREATE FUNCTION catalog.reject_homepage_decision_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'synthetic decision audit failure'; END $$;
 CREATE TRIGGER reject_homepage_decision_audit BEFORE INSERT ON catalog.audit FOR EACH ROW
 WHEN (NEW.audit_action='catalog.teacher-sites.decision-bind.v1')
 EXECUTE FUNCTION catalog.reject_homepage_decision_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", manualSite.SiteID, key, bind); err == nil {
		t.Fatal("audit failure committed the decision")
	}
	pending, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Query: "Legacy", Limit: 10})
	if err != nil || len(pending.Items) != 1 || pending.Items[0].DecisionRevision != 0 || pending.Items[0].Decision != nil {
		t.Fatalf("audit failure changed review state: %+v %v", pending, err)
	}
	if _, err := s.Read(ctx, p, "legacy"); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("audit failure published the teacher: %v", err)
	}
	if report, err := s.FlushPendingProjection(ctx, p, "school"); err != nil || report != nil {
		t.Fatalf("audit failure queued a projection rebuild: %+v %v", report, err)
	}
	if _, err := migration.Exec(`DROP TRIGGER reject_homepage_decision_audit ON catalog.audit`); err != nil {
		t.Fatal(err)
	}

	// Independent service instances must share the database lock and return the same receipt.
	repo := s.Repo.(*postgres.Repository)
	other := &c.Service{Repo: &postgres.Repository{DB: repo.DB, RequireTeacherDirectory: true, RequireDirectoryProjection: true}}
	const callers = 6
	var revisions [callers]int64
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			out, err := other.SetTeacherSiteDecision(ctx, p, "school", manualSite.SiteID, key, bind)
			if err != nil || out.Decision == nil || out.Decision.Action != "bind" || out.Decision.TeacherID != "legacy" || out.DecisionRevision <= 0 {
				t.Errorf("bind or replay: %+v %v", out, err)
			}
			revisions[i] = out.DecisionRevision
		})
	}
	wg.Wait()
	for _, rev := range revisions[1:] {
		if rev != revisions[0] {
			t.Fatalf("concurrent replays returned different revisions: %v", revisions)
		}
	}
	var audits int
	if err := repo.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog.audit
 WHERE audit_action='catalog.teacher-sites.decision-bind.v1' AND result='success' AND audit_actor=$1 AND audit_school='school'`,
		p.Subject).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("successful binding audit count: %d %v", audits, err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", manualSite.SiteID, key, teacherHomepageDecision("reject", "", 0)); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("changed idempotent body: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", manualSite.SiteID, "stale", teacherHomepageDecision("reject", "", 0)); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", rejectSite.SiteID, "duplicate-target", bind); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("two sites bound to the same teacher: %v", err)
	}

	foreign := p
	foreign.Schools = []string{"other"}
	if _, err := s.Import(ctx, foreign, "seed-other", c.Import{
		SchoolID: "other", SourceID: "other-source", Revision: 1, FetchedAt: time.Now(),
		Objects: []c.Object{
			{ID: "other", Kind: "school", SchoolID: "other", Name: "Other School"},
			{ID: "other-teacher", Kind: "teacher", SchoolID: "other", Name: "Foreign Teacher"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", rejectSite.SiteID, "foreign-target", teacherHomepageDecision("bind", "other-teacher", 0)); !errors.Is(err, c.ErrNotFound) && !errors.Is(err, c.ErrConflict) {
		t.Fatalf("binding selected a teacher outside the school: %v", err)
	}
	if report, err := s.FlushPendingProjection(ctx, p, "school"); err != nil || report == nil || report.Marks.Official != 2 {
		t.Fatalf("decision did not queue projection rebuild: %+v %v", report, err)
	}
	if _, err := s.Read(ctx, p, "legacy"); err != nil {
		t.Fatalf("published binding did not expose the teacher: %v", err)
	}
}

func TestTeacherHomepagePostgresRefreshPreservesDecisions(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	p.CanReadTeacherSites, p.CanRefreshTeacherSites = true, true
	ctx := context.Background()
	in := teacherHomepagePostgresSnapshot(t, s, p)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "initial", in)
	automatic, boundSite, rejectedSite := in.Sites[0], in.Sites[1], in.Sites[2]
	binding, err := s.SetTeacherSiteDecision(ctx, p, "school", boundSite.SiteID, "manual-bind", teacherHomepageDecision("bind", "legacy", 0))
	if err != nil {
		t.Fatal(err)
	}
	rejection, err := s.SetTeacherSiteDecision(ctx, p, "school", rejectedSite.SiteID, "manual-reject", teacherHomepageDecision("reject", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if report, err := s.FlushPendingProjection(ctx, p, "school"); err != nil || report == nil || report.Marks.Official != 2 {
		t.Fatalf("publish manual binding: %+v %v", report, err)
	}
	photoID := boundSite.Photo.ID
	if photo, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); err != nil || !bytes.Equal(photo.Bytes, boundSite.Photo.Bytes) {
		t.Fatalf("bound portrait: %+v %v", photo, err)
	}

	// The next crawl drops the bound site and now proposes an automatic
	// association for the explicitly rejected site.
	rejectedSite.Reason = ""
	rejectedSite.Unit = "Department"
	in.ObservedAt = in.ObservedAt.Add(time.Microsecond)
	in.Revision = in.ObservedAt.UnixMicro()
	in.Sites = []c.TeacherSite{automatic, rejectedSite}
	in.Matches = append(in.Matches, c.TeacherSiteMatch{TeacherID: "teacher", Name: "Teacher", DepartmentID: "dept", URL: rejectedSite.URL})
	in.Teachers[1] = c.TeacherSiteTeacher{TeacherID: "legacy", Reason: "name_absent"}
	in.Teachers[2].Reason = ""
	out := publishTeacherHomepagePostgresSnapshot(t, s, p, "site-removed", in)
	if out.Counts.Official != 1 || out.Counts.Eligible != 2 {
		t.Fatalf("removal and rejection projection: %+v", out.Counts)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("removed site's portrait remains available: %v", err)
	}
	if _, err := s.Read(ctx, p, "legacy"); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("removed site still provides directory eligibility: %v", err)
	}
	decided, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Status: "decided", Query: "Legacy", Limit: 10})
	if err != nil || len(decided.Items) != 1 {
		t.Fatalf("decision disappeared with site: %+v %v", decided, err)
	}
	stored := decided.Items[0]
	if stored.SiteID != boundSite.SiteID || stored.Present || stored.Reason != "site_unavailable" || stored.MatchedTeacherID != "" ||
		stored.Decision == nil || stored.Decision.TeacherID != "legacy" || stored.DecisionRevision != binding.DecisionRevision {
		t.Fatalf("removed site state: %+v", stored)
	}
	rejectRows, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Status: "decided", Query: "Teacher", Limit: 10})
	if err != nil || len(rejectRows.Items) != 1 || rejectRows.Items[0].MatchedTeacherID != "" ||
		rejectRows.Items[0].Decision == nil || rejectRows.Items[0].Decision.Action != "reject" ||
		rejectRows.Items[0].DecisionRevision != rejection.DecisionRevision {
		t.Fatalf("automatic refresh overrode rejection: %+v %v", rejectRows, err)
	}

	// Exact site reappearance restores the binding. The derived automatic
	// match to a different directory object must wait for manual revocation.
	boundSite.Reason, boundSite.Name, boundSite.Profile.Name = "", "Teacher", "Teacher"
	boundSite.Unit = "Department"
	boundSite.CandidateTeacherIDs = []string{"teacher"}
	rejectedSite.Reason = "unit_mismatch"
	rejectedSite.Unit = "Another Department"
	in.ObservedAt = in.ObservedAt.Add(time.Microsecond)
	in.Revision = in.ObservedAt.UnixMicro()
	in.Sites = []c.TeacherSite{automatic, boundSite, rejectedSite}
	in.Matches[1].URL = boundSite.URL
	in.Teachers[2].CandidateSiteIDs = []string{boundSite.SiteID, rejectedSite.SiteID}
	in.Teachers[2].CandidateCount = 2
	out = publishTeacherHomepagePostgresSnapshot(t, s, p, "site-restored", in)
	if out.Counts.Official != 2 || out.Counts.Eligible != 3 {
		t.Fatalf("restored manual eligibility: %+v", out.Counts)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); err != nil {
		t.Fatalf("restored manual portrait: %v", err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "teacher", photoID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("automatic match displaced the manual owner: %v", err)
	}
	rankedQuery := c.DirectoryQuery{Filter: c.Filter{Kind: "teacher", Limit: 20}}
	assertRankedCount := func(expected int64) {
		t.Helper()
		ranked, err := s.ReadDirectory(ctx, p, rankedQuery)
		if err != nil || ranked.Total != expected || int64(len(ranked.Items)) != expected {
			t.Fatalf("ranked teachers: %+v %v", ranked, err)
		}
		options, err := s.ReadFilterOptions(ctx, p, "teacher", "")
		if err != nil || options.Total != expected {
			t.Fatalf("teacher filter options: %+v %v", options, err)
		}
	}
	assertRankedCount(3) // Populate the current generation's selection cache.

	revoked, err := s.SetTeacherSiteDecision(ctx, p, "school", boundSite.SiteID, "manual-revoke",
		teacherHomepageDecision("revoke", "", binding.DecisionRevision))
	if err != nil || revoked.Decision != nil || revoked.DecisionRevision <= binding.DecisionRevision {
		t.Fatalf("revoke binding: %+v %v", revoked, err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("revoked manual account still has portrait: %v", err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "teacher", photoID); err != nil {
		t.Fatalf("current automatic match did not take effect on revoke: %v", err)
	}
	if _, err := s.Read(ctx, p, "legacy"); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("stale generation still exposes the revoked teacher: %v", err)
	}
	list, err := s.ReadList(ctx, p, c.Filter{Kind: "teacher", Limit: 20})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("stale generation lists the revoked teacher: %+v %v", list, err)
	}
	assertRankedCount(2)
	lookup, err := s.ReadLookup(ctx, p, []string{"legacy", "teacher"})
	if err != nil || len(lookup.Items) != 1 || len(lookup.MissingIDs) != 1 || lookup.MissingIDs[0] != "legacy" {
		t.Fatalf("stale generation can resolve the revoked teacher: %+v %v", lookup, err)
	}
	documents, err := s.DiscoveryCurrent(ctx, p, []string{"legacy", "teacher"})
	if err != nil || len(documents) != 1 || documents[0].Target.ID != "teacher" {
		t.Fatalf("revoked teacher available through search: %+v %v", documents, err)
	}
	old, err := s.SetTeacherSiteDecision(ctx, p, "school", boundSite.SiteID, "manual-bind", teacherHomepageDecision("bind", "legacy", 0))
	if err != nil || old.Decision == nil || old.DecisionRevision != binding.DecisionRevision || old.Decision.TeacherID != "legacy" {
		t.Fatalf("replay lost original receipt after revoke: %+v %v", old, err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("replay restored a revoked binding: %v", err)
	}

	// A newer snapshot with invalid portrait bytes must leave the last valid
	// profile, review and directory state available.
	in.ObservedAt = in.ObservedAt.Add(time.Microsecond)
	in.Revision = in.ObservedAt.UnixMicro()
	badBytes := []byte("synthetic invalid PNG")
	badSum := sha256.Sum256(badBytes)
	badID := hex.EncodeToString(badSum[:])
	in.Sites[1].Photo = &c.TeacherHomepageImage{ID: badID, MediaType: "image/png", Width: 2, Height: 3}
	in.ReadPhoto = func(_ context.Context, id string) ([]byte, error) {
		if id != badID {
			return nil, c.ErrInput
		}
		return badBytes, nil
	}
	if _, err := s.ImportTeacherSites(ctx, p, in); !errors.Is(err, c.ErrInput) {
		t.Fatalf("invalid photo did not abort refresh: %v", err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "teacher", photoID); err != nil {
		t.Fatalf("invalid refresh removed previous portrait: %v", err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "teacher", badID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("invalid portrait became visible: %v", err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", boundSite.SiteID, "manual-rebind",
		teacherHomepageDecision("bind", "legacy", revoked.DecisionRevision)); err != nil {
		t.Fatalf("restore a just-revoked match: %v", err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, p, "legacy", photoID); err != nil {
		t.Fatalf("pending revocation hid the restored portrait: %v", err)
	}
	assertRankedCount(3)
}
