package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestHomepagePartialScopeAndReplay(t *testing.T) {
	s, p, db, _ := projectionFixture(t)
	p.CanRefreshTeacherSites = true
	in := teacherHomepagePostgresSnapshot(t, s, p)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "initial", in)
	ctx := context.Background()
	scope, err := s.HomepageDetailScope(ctx, p, in.SchoolID)
	if err != nil || scope.Total != 3 {
		t.Fatalf("scope %+v %v", scope, err)
	}
	matched := 0
	for _, site := range scope.Sites {
		if site.TeacherID != "" {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("matched %d", matched)
	}
	row := in.Sites[0]
	patch := c.HomepageDetailPatch{SchoolID: scope.SchoolID, SourceRevision: scope.SourceRevision, ReviewRevision: scope.ReviewRevision,
		SiteID: row.SiteID, URL: row.URL, TeacherID: "official", CheckedAt: in.ObservedAt.Add(time.Second),
		PageStatus: "available", Set: map[string]json.RawMessage{"biography": json.RawMessage(`"Home text"`)}}
	if err := s.ApplyHomepageDetailPatch(ctx, c.Principal{}, patch); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	bad := patch
	bad.TeacherID = "legacy"
	if err := s.ApplyHomepageDetailPatch(ctx, p, bad); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.ApplyHomepageDetailPatch(ctx, p, patch); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.ReadTeacherHomepageWithContacts(ctx, p, "official")
	if err != nil || out.Profile == nil || out.Profile.Biography != "Home text" || out.Profile.Email == "" || out.Profile.Photo == nil || len(out.Profile.Sections) != 1 {
		t.Fatal(out, err)
	}
	bad = patch
	bad.Set = map[string]json.RawMessage{"research": json.RawMessage(`"conflict"`)}
	if err := s.ApplyHomepageDetailPatch(ctx, p, bad); !errors.Is(err, c.ErrConflict) {
		t.Fatal(err)
	}
	patch.Set = map[string]json.RawMessage{"email": json.RawMessage("null")}
	patch.CheckedAt = patch.CheckedAt.Add(time.Second)
	if err := s.ApplyHomepageDetailPatch(ctx, p, patch); err != nil {
		t.Fatal(err)
	}
	out, err = s.ReadTeacherHomepageWithContacts(ctx, p, "official")
	if err != nil || out.Profile == nil || out.Profile.Email != "" || out.Profile.Phone == "" || out.Profile.Biography != "Home text" {
		t.Fatal(out, err)
	}
	patch.PageStatus = "not_found"
	patch.Set = nil
	patch.CheckedAt = patch.CheckedAt.Add(time.Second)
	for i := 0; i < 2; i++ {
		if err := s.ApplyHomepageDetailPatch(ctx, p, patch); err != nil {
			t.Fatal(err)
		}
	}
	var intact bool
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM catalog.teacher_homepage_sites)=3 AND
 (SELECT count(*) FROM catalog.teacher_unmatched_sites)=2 AND (SELECT count(*) FROM catalog.teacher_homepage_photos)=1`).Scan(&intact); err != nil || !intact {
		t.Fatal(intact, err)
	}
}
