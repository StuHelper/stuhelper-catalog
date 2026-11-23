package catalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestLegacyTextRestoreChecksMembershipAndReplays(t *testing.T) {
	s, p, db, _ := projectionFixture(t)
	ctx := context.Background()
	in := teacherHomepagePostgresSnapshot(t, s, p)
	for i := range in.Sites {
		site := &in.Sites[i]
		site.Photo = nil
		site.Profile = c.TeacherHomepage{Name: site.Name, Biography: "Offline biography"}
	}
	legacy := in
	legacy.Version, legacy.Sites, legacy.Teachers = 0, nil, nil
	if _, err := s.ImportTeacherSites(ctx, p, legacy); err != nil {
		t.Fatal(err)
	}
	pending := c.TeacherUnmatchedSnapshot{SchoolID: in.SchoolID, Revision: in.Revision, ObservedAt: in.ObservedAt}
	for i, raw := range []string{"manual", "reject"} {
		site := in.Sites[i+1]
		pending.Items = append(pending.Items, c.UnmatchedTeacherSite{SiteID: raw, Name: site.Name, Unit: site.Unit, Reason: site.Reason, CandidateCount: site.CandidateCount})
	}
	if err := s.ImportTeacherUnmatched(ctx, p, pending); err != nil {
		t.Fatal(err)
	}
	checked := map[string]time.Time{}
	for _, site := range in.Sites {
		checked[site.SiteID] = in.ObservedAt.Add(-24 * time.Hour)
	}
	sum := strings.Repeat("a", 64)
	wrong := in
	wrong.Sites = append([]c.TeacherSite{}, in.Sites...)
	wrong.Sites[1].Unit = "Changed"
	if err := s.RestoreTeacherHomepageText(ctx, p, wrong, sum, checked); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("changed pending site: %v", err)
	}
	var sites int
	if err := db.QueryRow("SELECT count(*) FROM catalog.teacher_homepage_sites").Scan(&sites); err != nil || sites != 0 {
		t.Fatalf("partial restore: %d %v", sites, err)
	}
	if err := s.RestoreTeacherHomepageText(ctx, c.Principal{}, in, sum, checked); !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("unscoped restore: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RestoreTeacherHomepageText(ctx, p, in, sum, checked); err != nil {
			t.Fatal(err)
		}
		out, err := s.ReadTeacherHomepage(ctx, p, "official")
		if err != nil || out.Profile == nil || out.Profile.Biography != "Offline biography" || out.Profile.Photo != nil {
			t.Fatalf("public text: %+v %v", out, err)
		}
	}
	var unchanged bool
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM catalog.teacher_unmatched_sites)=2
 AND (SELECT count(*) FROM catalog.teacher_homepage_photos)=0
 AND (SELECT count(*) FROM catalog.teacher_homepage_body_refs)=0
 AND (SELECT revision FROM catalog.teacher_homepage_review_state WHERE school_id='school')=1
 AND (SELECT legacy_text_receipt->>'file_sha256'=$1 AND revision=$2 AND observed_at=$3
 FROM catalog.teacher_site_sources WHERE school_id='school')`, sum, in.Revision, in.ObservedAt).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("restore invariants: %v %v", unchanged, err)
	}
	if err := s.RestoreTeacherHomepageText(ctx, p, in, strings.Repeat("b", 64), checked); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("different file: %v", err)
	}
}

func TestReviewedLegacyAmbiguityPreservesMarkerUntilAdminBinding(t *testing.T) {
	s, p, db, _ := projectionFixture(t)
	p.CanReadTeacherSites = true
	ctx := context.Background()
	in := teacherHomepagePostgresSnapshot(t, s, p)
	for i := range in.Sites {
		site := &in.Sites[i]
		site.Photo = nil
		site.Profile = c.TeacherHomepage{Name: site.Name, Biography: "Historical text"}
	}
	held, pending := in.Sites[1], in.Sites[2]
	in.Sites[1].Reason = "ambiguous_catalog"
	in.Teachers[1].Reason = "ambiguous_catalog"
	legacy := in
	legacy.Sites, legacy.Teachers, legacy.Version = nil, nil, 0
	legacy.Matches = append(append([]c.TeacherSiteMatch{}, in.Matches...), c.TeacherSiteMatch{TeacherID: "legacy", Name: "Legacy", DepartmentID: "dept", URL: held.URL})
	if _, err := s.ImportTeacherSites(ctx, p, legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTeacherUnmatched(ctx, p, c.TeacherUnmatchedSnapshot{SchoolID: in.SchoolID, Revision: in.Revision, ObservedAt: in.ObservedAt,
		Items: []c.UnmatchedTeacherSite{{SiteID: "reject", Name: pending.Name, Unit: pending.Unit, Reason: pending.Reason, CandidateCount: pending.CandidateCount}}}); err != nil {
		t.Fatal(err)
	}
	dates := map[string]time.Time{}
	for _, site := range in.Sites {
		dates[site.SiteID] = in.ObservedAt
	}
	review := []c.LegacyHomepageReview{{SiteID: held.SiteID, TeacherID: "legacy", DepartmentID: "dept", Reason: "ambiguous_catalog"}}
	restore := func(items []c.LegacyHomepageReview) error {
		return s.RestoreTeacherHomepageTextWithReview(ctx, p, in, strings.Repeat("b", 64), dates, items)
	}
	if err := restore(nil); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := restore(review); err != nil {
			t.Fatal(err)
		}
	}
	if err := restore(nil); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	checkHeld := func() {
		var ok bool
		err := db.QueryRow(`SELECT (SELECT official AND homepage_site_id='' AND homepage_url=$1 FROM catalog.teacher_directory WHERE teacher_id='legacy')
 AND (SELECT count(*) FROM catalog.teacher_unmatched_sites)=1 AND (SELECT jsonb_array_length(legacy_text_receipt->'held') FROM catalog.teacher_site_sources WHERE school_id='school')=1`, held.URL).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("unlinked legacy marker: %v %v", ok, err)
		}
	}
	checkHeld()
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", pending.SiteID, "other-site", teacherHomepageDecision("reject", "", 0)); err != nil {
		t.Fatal(err)
	}
	checkHeld()
	page, err := s.ReadTeacherSiteReviews(ctx, p, c.TeacherReviewQuery{SchoolID: "school", Status: "pending", Query: held.SiteID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].MatchedTeacherID != "" {
		t.Fatal(page, err)
	}
	if _, err := s.SetTeacherSiteDecision(ctx, p, "school", held.SiteID, "review-binding", teacherHomepageDecision("bind", "legacy", 0)); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReadTeacherHomepage(ctx, p, "legacy")
	if err != nil || out.Profile == nil || out.Profile.Biography != "Historical text" {
		t.Fatal(out, err)
	}
}
