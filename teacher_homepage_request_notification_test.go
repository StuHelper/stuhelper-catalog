package catalog_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

func TestTeacherHomepageRequestNotificationUsesOriginalPrivateReceipt(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := homepageFixture(t)
	ownership := &homepageOwnershipFixture{}
	service.TeacherHomepageOwnership = ownership
	handler := homepageHandler(service)
	input := homepageSubmitBody("correction", "email", homepageProposedEmail, homepagePrivateNote)
	pending := homepageAssertPending(t, homepageCall(t, handler, http.MethodPost, homepageOwnRequests, homepageOwner, "request", input, http.StatusOK))

	if _, send, err := service.TeacherHomepageRequestNotification(ctx, pending.ID, pending.Revision); send || err != nil {
		t.Fatalf("pending request generated a notification: send=%v err=%v", send, err)
	}
	const decision = `{"action":"approve","expected_revision":1,"expected_homepage_revision":0,"note":"Synthetic approval"}`
	out := homepageCall(t, handler, http.MethodPut, homepageAdminRequests+"/"+pending.ID+"/decision", homepageAdmin, "approve", decision, http.StatusOK)
	resolved := homepageDecode[homepageRequestReceipt](t, out)
	for _, test := range []struct {
		actor      string
		authorized bool
	}{
		{homepageReader, false},
		{homepageAdmin, false},
		{homepageOwner, true},
	} {
		ids, err := service.AccessibleTeacherHomepageRequests(ctx, test.actor, []string{resolved.ID})
		if err != nil || slices.Contains(ids, resolved.ID) != test.authorized {
			t.Fatalf("unexpected notification access: scenario=%v err=%v", test.authorized, err)
		}
	}
	if _, send, err := service.TeacherHomepageRequestNotification(ctx, pending.ID, pending.Revision); send || err != nil {
		t.Fatalf("stale request revision generated a notification: send=%v err=%v", send, err)
	}

	ownership.revoked.Store(true)
	target, send, err := service.TeacherHomepageRequestNotification(ctx, resolved.ID, resolved.Revision)
	if err != nil || !send || target.Owner != homepageOwner || target.SchoolID != "school" ||
		target.TeacherID != resolved.TeacherID || target.Status != "approved" {
		t.Fatalf("terminal notification no longer addressed the original owner: send=%v err=%v", send, err)
	}
	if ids, err := service.AccessibleTeacherHomepageRequests(ctx, homepageOwner, []string{resolved.ID}); err != nil || len(ids) != 0 {
		t.Fatalf("revoked ownership left the notification target accessible: count=%d err=%v", len(ids), err)
	}
	homepageCall(t, handler, http.MethodGet, homepageOwnDetailBase+resolved.ID, homepageOwner, "", "", http.StatusForbidden)
}
