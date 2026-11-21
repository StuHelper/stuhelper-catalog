package catalog_test

import (
	"context"
	"errors"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestTeacherHomepageComplaintHideRevisionAndReplay(t *testing.T) {
	s, admin, _, snapshot := homepageFixture(t)
	ctx := context.Background()
	caller := c.Principal{Subject: "synthetic-unverified", Schools: []string{"school"}}
	if _, err := s.InspectTeacherHomepageComplaint(ctx, c.Principal{}, "school", "official"); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal("anonymous target inspection", err)
	}
	if _, err := s.InspectTeacherHomepageComplaint(ctx, caller, "other", "official"); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("cross-school target inspection", err)
	}
	if _, err := s.InspectTeacherHomepageComplaint(ctx, caller, "school", "dept"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("department accepted as teacher", err)
	}
	if _, err := s.InspectTeacherHomepageComplaint(ctx, caller, "school", "legacy"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("login-only complaint reveals a directory teacher without a published homepage", err)
	}
	target, err := s.InspectTeacherHomepageComplaint(ctx, caller, "school", "official")
	if err != nil || !target.Visible || target.TeacherID != "official" || target.Revision < 1 || target.Revision > 1<<53-1 {
		t.Fatal("invalid hosted-school target", target, err)
	}
	if err := s.ApplyTeacherHomepageComplaintHide(ctx, caller, "school", "official", "hide", target.Revision); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("logged-in reporter could hide the profile", err)
	}

	latestSnapshot := homepageNextSnapshot(snapshot)
	latestSnapshot.Sites[0].Profile.Biography = "New source revision for complaint review"
	publishTeacherHomepagePostgresSnapshot(t, s, admin, "complaint-new-source", latestSnapshot)
	latest, err := s.InspectTeacherHomepageComplaint(ctx, caller, "school", "official")
	if err != nil || !latest.Visible || latest.Revision == target.Revision {
		t.Fatal("source refresh did not invalidate the decision revision", latest, err)
	}
	if err := s.ApplyTeacherHomepageComplaintHide(ctx, admin, "school", "official", "stale", target.Revision); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale moderation decision modified newer data", err)
	}
	for range 2 {
		if err := s.ApplyTeacherHomepageComplaintHide(ctx, admin, "school", "official", "hide", latest.Revision); err != nil {
			t.Fatal("idempotent complaint hide", err)
		}
	}
	if err := s.ApplyTeacherHomepageComplaintHide(ctx, admin, "school", "official", "hide", target.Revision); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	state, err := s.ReadTeacherHomepageControls(ctx, admin, "school", "official")
	if err != nil || state.Controls.Revision != 1 || state.Controls.PageVisibility != c.TeacherHomepageHidden {
		t.Fatal("complaint decision was not committed once", state, err)
	}

	teacher, err := s.ReadWithTeacherHomepage(ctx, admin, "official")
	if err != nil || teacher.TeacherHomepage != nil || teacher.Kind != "teacher" || teacher.DepartmentID != "dept" {
		t.Fatal("homepage hide changed ordinary directory facts", teacher, err)
	}
	if _, err := s.ReadTeacherHomepagePhoto(ctx, admin, "official", snapshot.Sites[0].Photo.ID); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("old direct portrait still accessible after complaint", err)
	}
	hidden, err := s.InspectTeacherHomepageComplaint(ctx, caller, "school", "official")
	if err != nil || hidden.Visible || hidden.Revision == latest.Revision {
		t.Fatal("hidden homepage lost complaint evidence or kept its public revision", hidden, err)
	}
}
