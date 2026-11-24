package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestExcludeSitePortraitChecksEveryReference(t *testing.T) {
	s, p, db, _ := projectionFixture(t)
	ctx := context.Background()
	p.CanRefreshTeacherSites = true
	in := teacherHomepagePostgresSnapshot(t, s, p)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "portrait-snapshot", in)
	photo := in.Sites[0].Photo
	sites := []string{in.Sites[0].SiteID, in.Sites[1].SiteID}
	p.CanMaintain = true
	read := func() *c.TeacherHomepagePublicProfile {
		out, err := s.ReadTeacherHomepage(ctx, p, "official")
		if err != nil || out.Profile == nil {
			t.Fatal(out, err)
		}
		return out.Profile
	}
	before := read()
	if before.Photo == nil {
		t.Fatal("missing fixture image")
	}
	if err := s.ExcludeSitePortrait(ctx, c.Principal{}, in.SchoolID, photo.ID, sites); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.ExcludeSitePortrait(ctx, p, in.SchoolID, photo.ID, sites[:1]); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("mismatched scope changed publication")
	}
	for i := 0; i < 2; i++ {
		if err := s.ExcludeSitePortrait(ctx, p, in.SchoolID, photo.ID, sites); err != nil {
			t.Fatal(err)
		}
	}
	before.Photo = nil
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("non-photo publication changed")
	}
	var audits, photos int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM catalog.audit WHERE audit_action='catalog.teacher-portrait.excluded.v1'),(SELECT count(*) FROM catalog.teacher_homepage_photos)`).Scan(&audits, &photos); err != nil || audits != 2 || photos != 0 {
		t.Fatal(audits, photos, err)
	}
	scope, err := s.HomepageDetailScope(ctx, p, in.SchoolID)
	if err != nil {
		t.Fatal(err)
	}
	patch := c.HomepageDetailPatch{SchoolID: in.SchoolID, SiteID: sites[0], TeacherID: "official", URL: in.Sites[0].URL, SourceRevision: scope.SourceRevision, ReviewRevision: scope.ReviewRevision, CheckedAt: in.ObservedAt.Add(time.Second), PageStatus: "available", Photo: photo}
	if err := s.ApplyHomepageDetailPatch(ctx, p, patch); err == nil {
		t.Fatal("reimported excluded portrait")
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("failed import changed publication")
	}
}
