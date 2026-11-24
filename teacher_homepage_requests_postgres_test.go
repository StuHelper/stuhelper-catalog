package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

const (
	homepageOwnRequests   = homepageObject + "/teacher-homepage/requests"
	homepageOwnDetailBase = "/api/v1/catalog/me/teacher-homepage/requests/"
	homepageAdminRequests = homepageAdminBase + "/requests"
	homepageProposedEmail = "verified-teacher@example.org"
	homepagePrivateNote   = "Synthetic ownership context kept in the private request"
	homepageSubmitAction  = "catalog.teacher-homepage-request.submitted.v1"
	homepageResolveAction = "catalog.teacher-homepage-request.resolved.v1"
)

type homepageOwnershipFixture struct {
	revoked     atomic.Bool
	unavailable atomic.Bool
	teacher     string
}

func (a *homepageOwnershipFixture) OwnsTeacher(_ context.Context, subject, school, teacher string) (bool, error) {
	if a.unavailable.Load() {
		return false, c.ErrUnavailable
	}
	target := a.teacher
	if target == "" {
		target = "official"
	}
	return !a.revoked.Load() && school == "school" && subject == homepageOwner && teacher == target, nil
}

type homepageRequestReceipt struct {
	ID             string `json:"id"`
	TeacherID      string `json:"teacher_id"`
	Kind           string `json:"kind"`
	Message        string `json:"message"`
	Revision       int64  `json:"revision"`
	Status         string `json:"status"`
	ResolvedAt     string `json:"resolved_at"`
	ResolutionNote string `json:"resolution_note"`
}

type homepageRequestPage struct {
	Items []homepageRequestReceipt `json:"items"`
	Total int64                    `json:"total"`
	Next  string                   `json:"next"`
}

func homepageSubmitBody(kind, field, value, message string) string {
	body := map[string]any{"kind": kind, "field": map[string]string{"field": field}, "message": message}
	if value != "" {
		body["value"] = map[string]string{"text": value}
	}
	data, _ := json.Marshal(body)
	return string(data)
}

func homepageAssertPending(t *testing.T, out *httptest.ResponseRecorder) homepageRequestReceipt {
	t.Helper()
	req := homepageDecode[homepageRequestReceipt](t, out)
	if !c.ValidID(req.ID) || req.TeacherID != "official" || req.Revision != 1 || req.Status != "pending" || req.ResolvedAt != "" {
		t.Fatalf("invalid pending request receipt: %+v", req)
	}
	return req
}

func TestTeacherHomepageRequestsPostgresAuthorizationAndReview(t *testing.T) {
	s, _, _, _ := homepageFixture(t)
	ownership := &homepageOwnershipFixture{}
	s.TeacherHomepageOwnership = ownership
	h := homepageHandler(s)
	email := homepageSubmitBody("correction", "email", homepageProposedEmail, homepagePrivateNote)
	for _, check := range []struct {
		actor  string
		status int
	}{{"", http.StatusUnauthorized}, {homepageReader, http.StatusForbidden}, {homepageAdmin, http.StatusForbidden}} {
		homepageCall(t, h, http.MethodPost, homepageOwnRequests, check.actor, "submit-unowned", email, check.status)
	}
	homepageCall(t, h, http.MethodPost, "/api/v1/catalog/objects/teacher/teacher-homepage/requests", homepageOwner, "submit-for-another-teacher", email, http.StatusForbidden)
	for _, target := range []string{"legacy", "missing-teacher"} {
		homepageCall(t, h, http.MethodPost, "/api/v1/catalog/objects/"+target+"/teacher-homepage/requests", homepageReader, "submit-unowned", email, http.StatusForbidden)
	}
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "", email, http.StatusBadRequest)
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "missing-value", homepageSubmitBody("correction", "email", "", homepagePrivateNote), http.StatusBadRequest)
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "invalid-value", homepageSubmitBody("hide", "email", homepageProposedEmail, homepagePrivateNote), http.StatusBadRequest)
	ownership.unavailable.Store(true)
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "owned", email, http.StatusServiceUnavailable)
	ownership.unavailable.Store(false)

	first := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "owned", email, http.StatusOK))
	hidden := homepageSubmitBody("hide", "phone", "", "Remove the outdated office phone")
	second := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "hide", hidden, http.StatusOK))
	supplement := homepageSubmitBody("supplement", "position", "Department outreach coordinator", "Add a current position")
	third := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "supplement", supplement, http.StatusOK))
	if first.ID == second.ID || second.ID == third.ID || third.ID == first.ID {
		t.Fatal("distinct requests reused an identity")
	}
	for _, actor := range []string{homepageReader, "issuer:school-reader", "issuer:other-admin"} {
		homepageCall(t, h, http.MethodGet, homepageAdminRequests, actor, "", "", http.StatusForbidden)
		homepageCall(t, h, http.MethodGet, homepageAdminRequests+"/"+first.ID, actor, "", "", http.StatusForbidden)
		homepageCall(t, h, http.MethodPut, homepageAdminRequests+"/"+first.ID+"/decision", actor, "deny-review", `{"action":"reject","expected_revision":1,"note":"Synthetic decision"}`, http.StatusForbidden)
	}
	pageURL := homepageAdminRequests + "?limit=1"
	page := homepageDecode[homepageRequestPage](t, homepageCall(t, h, http.MethodGet, pageURL, homepageAdmin, "", "", http.StatusOK))
	if page.Total != 3 || len(page.Items) != 1 || page.Next == "" {
		t.Fatalf("invalid queue first page: %+v", page)
	}
	tail := homepageDecode[homepageRequestPage](t, homepageCall(t, h, http.MethodGet, homepageAdminRequests+"?limit=2&after="+url.QueryEscape(page.Next), homepageAdmin, "", "", http.StatusOK))
	if tail.Total != 3 || len(tail.Items) != 2 || tail.Next != "" ||
		tail.Items[0].ID == page.Items[0].ID || tail.Items[1].ID == page.Items[0].ID || tail.Items[1].ID == tail.Items[0].ID {
		t.Fatalf("queue cursor lost or duplicated a request: %+v", tail)
	}
	homepageCall(t, h, http.MethodGet, pageURL+"&status=rejected&after="+url.QueryEscape(page.Next), homepageAdmin, "", "", http.StatusBadRequest)

	detail := homepageOwnDetailBase + first.ID
	homepageCall(t, h, http.MethodGet, detail, homepageReader, "", "", http.StatusNotFound)
	homepageCall(t, h, http.MethodGet, detail, homepageAdmin, "", "", http.StatusNotFound)
	homepageCall(t, h, http.MethodGet, detail, homepageOwner, "", "", http.StatusOK)
	ownership.revoked.Store(true)
	homepageCall(t, h, http.MethodGet, detail, homepageOwner, "", "", http.StatusForbidden)
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "owned", email, http.StatusForbidden)
	ownership.revoked.Store(false)

	const approval = `{"action":"approve","expected_revision":1,"expected_homepage_revision":0,"note":"Institute confirmed this correction"}`
	change := homepageAdminRequests + "/" + first.ID + "/decision"
	ownership.revoked.Store(true)
	homepageCall(t, h, http.MethodPut, change, homepageAdmin, "approve", approval, http.StatusForbidden)
	ownership.revoked.Store(false)
	approved := homepageDecode[homepageRequestReceipt](t, homepageCall(t, h, http.MethodPut, change, homepageAdmin, "approve", approval, http.StatusOK))
	if approved.Status != "approved" || approved.Revision != 2 || approved.ResolvedAt == "" {
		t.Fatalf("invalid approval: %+v", approved)
	}
	ownReceipt := homepageDecode[homepageRequestReceipt](t, homepageCall(t, h, http.MethodGet, detail, homepageOwner, "", "", http.StatusOK))
	if ownReceipt.Status != approved.Status || ownReceipt.ResolutionNote != approved.ResolutionNote {
		t.Fatalf("owner does not see the result and note: %+v", ownReceipt)
	}
	homepageCall(t, h, http.MethodPut, homepageAdminRequests+"/"+second.ID+"/decision", homepageAdmin, "stale-profile", approval, http.StatusConflict)
	const reject = `{"action":"reject","expected_revision":1,"note":"Keep the verified phone number"}`
	rejected := homepageDecode[homepageRequestReceipt](t, homepageCall(t, h, http.MethodPut, homepageAdminRequests+"/"+second.ID+"/decision", homepageAdmin, "reject", reject, http.StatusOK))
	if rejected.Revision != 2 || rejected.Status != "rejected" {
		t.Fatalf("invalid rejection: %+v", rejected)
	}
	pub := homepageCall(t, h, http.MethodGet, homepageObject+"?include_teacher_homepage=true", homepageReader, "", "", http.StatusOK)
	profile := homepagePublicProfile(t, h, homepagePublic+"?reveal_contacts=true")
	if profile == nil || profile.Email != homepageProposedEmail || profile.Phone != "555-0100" {
		t.Fatal("approval or rejection did not apply correctly")
	}
	if strings.Contains(pub.Body.String(), homepagePrivateNote) || strings.Contains(pub.Body.String(), ownReceipt.ResolutionNote) {
		t.Fatal("request annotations reached public output")
	}

	rev := homepageSet(t, h, 1, "hidden", "[]", "hide-after-decision")
	if rev <= 1 {
		t.Fatal("page hide did not advance the control revision")
	}
	own := homepageDecode[homepageRequestPage](t, homepageCall(t, h, http.MethodGet, homepageOwnRequests+"?status=approved", homepageOwner, "", "", http.StatusOK))
	if own.Total != 1 || len(own.Items) != 1 || own.Items[0].ID != first.ID {
		t.Fatalf("hidden page prevented owner from checking an approved request: %+v", own)
	}
	homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "under-hidden", hidden, http.StatusOK))
	rev = homepageSet(t, h, rev, "stopped", "[]", "stop-before-approval")
	supplementDecision := fmt.Sprintf(`{"action":"approve","expected_revision":1,"expected_homepage_revision":%d,"note":"Approved the revised office role"}`, rev)
	homepageCall(t, h, http.MethodPut, homepageAdminRequests+"/"+third.ID+"/decision", homepageAdmin, "approve-while-stopped", supplementDecision, http.StatusOK)
	if profile := homepagePublicProfile(t, h, homepagePublic); profile != nil {
		t.Fatal("request approval resumed a stopped homepage")
	}
}

func TestTeacherHomepageRequestsPostgresIndependentPortrait(t *testing.T) {
	checkRequestPortraitStorage(t, nil)
}

func checkRequestPortraitStorage(t *testing.T, store c.TeacherImageStorage) {
	t.Helper()
	s, p, _, _ := homepageFixtureWithImages(t, store)
	s.TeacherHomepageOwnership = &homepageOwnershipFixture{teacher: "legacy"}
	h := homepageHandler(s)
	const objectPath = "/api/v1/catalog/objects/legacy"
	const ownPath = objectPath + "/teacher-homepage/requests"
	homepageCall(t, h, http.MethodGet, objectPath, homepageReader, "", "", http.StatusNotFound)

	picture, err := json.Marshal(c.TeacherHomepageFieldPhoto{ContentType: "image/png", Data: homepagePortrait(t)})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"kind":"supplement","field":{"field":"photo"},"value":{"photo":%s},"message":"Use this verified teacher portrait"}`, picture)
	result := homepageCall(t, h, http.MethodPost, ownPath, homepageOwner, "propose-photo", body, http.StatusOK)
	proposal := homepageDecode[struct {
		homepageRequestReceipt
		Value struct {
			Photo c.TeacherHomepageFieldPhoto `json:"photo"`
		} `json:"value"`
	}](t, result)
	metadata := proposal.Value.Photo
	if proposal.ID == "" || proposal.TeacherID != "legacy" || proposal.Revision != 1 || proposal.Status != "pending" ||
		metadata.ID == "" || metadata.Width != 3 || metadata.Height != 1 || len(metadata.Data) != 0 || strings.Contains(result.Body.String(), `"data":`) {
		t.Fatalf("request must return only private portrait metadata: %+v", proposal)
	}
	preview := homepageAdminRequests + "/" + proposal.ID + "/photo"
	for _, actor := range []string{homepageReader, homepageOwner, "issuer:school-reader", "issuer:other-admin"} {
		homepageCall(t, h, http.MethodGet, preview, actor, "", "", http.StatusForbidden)
	}
	homepageCall(t, h, http.MethodGet, preview, "", "", "", http.StatusUnauthorized)
	photo := homepageCall(t, h, http.MethodGet, preview, homepageAdmin, "", "", http.StatusOK)
	decoded, err := png.Decode(photo.Body)
	if err != nil || decoded.Bounds().Dx() != 3 || decoded.Bounds().Dy() != 1 {
		t.Fatal("moderator could not inspect the requested image", err)
	}
	portraitURL := objectPath + "/teacher-homepage/photos/" + metadata.ID
	homepageCall(t, h, http.MethodGet, portraitURL, homepageReader, "", "", http.StatusNotFound)
	homepageCall(t, h, http.MethodGet, objectPath, homepageReader, "", "", http.StatusNotFound)

	approve := `{"action":"approve","expected_revision":1,"expected_homepage_revision":0,"note":"Approved after checking the teacher's identity"}`
	homepageCall(t, h, http.MethodPut, homepageAdminRequests+"/"+proposal.ID+"/decision", homepageAdmin, "approve-portrait", approve, http.StatusOK)
	if _, err := s.FlushPendingProjection(context.Background(), p, "school"); err != nil {
		t.Fatal("publish independently approved directory entry", err)
	}
	profile := homepagePublicProfile(t, h, objectPath+"/teacher-homepage")
	if profile == nil || profile.Photo == nil || profile.Photo.URL != portraitURL || profile.Photo.ContentType != "image/png" {
		t.Fatalf("approved independent portrait is unavailable: %+v", profile)
	}
	homepageCall(t, h, http.MethodHead, portraitURL, homepageReader, "", "", http.StatusOK)
	homepageCall(t, h, http.MethodGet, portraitURL, "", "", "", http.StatusUnauthorized)
}

func TestTeacherHomepageRequestsPostgresSubmissionQuota(t *testing.T) {
	s, _, _, _ := homepageFixture(t)
	s.TeacherHomepageOwnership = &homepageOwnershipFixture{}
	now := time.Now().UTC()
	s.Clock = func() time.Time { return now }
	h := homepageHandler(s)
	body := homepageSubmitBody("hide", "phone", "", "Remove this outdated phone number")
	for i := range 10 {
		homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, fmt.Sprintf("quota-%d", i), body, http.StatusOK))
	}
	homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "quota-10", body, http.StatusTooManyRequests)
	homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "quota-0", body, http.StatusOK))
	page := homepageDecode[homepageRequestPage](t, homepageCall(t, h, http.MethodGet, homepageOwnRequests, homepageOwner, "", "", http.StatusOK))
	if page.Total != 10 || len(page.Items) != 10 {
		t.Fatalf("rate limit or replay wrote an extra request: %+v", page)
	}
	now = now.Add(time.Minute + time.Second)
	homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "quota-10", body, http.StatusOK))
}

func TestTeacherHomepageRequestsPostgresDecisionAndOutboxAtomicity(t *testing.T) {
	s, _, migration, _ := homepageFixture(t)
	s.TeacherHomepageOwnership = &homepageOwnershipFixture{}
	h := homepageHandler(s)
	email := homepageSubmitBody("correction", "email", homepageProposedEmail, homepagePrivateNote)
	submitted := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "single-submission", email, http.StatusOK))
	repeat := homepageAssertPending(t, homepageCall(t, h, http.MethodPost, homepageOwnRequests, homepageOwner, "single-submission", email, http.StatusOK))
	if repeat.ID != submitted.ID || homepageAuditCount(t, migration, homepageSubmitAction, homepageOwner) != 1 {
		t.Fatal("submission replay changed the receipt or duplicated its audit entry")
	}
	requestURL := homepageAdminRequests + "/" + submitted.ID
	decision := requestURL + "/decision"
	const resolutionNote = "Synthetic private approval note"
	body := fmt.Sprintf(`{"action":"approve","expected_revision":1,"expected_homepage_revision":0,"note":%q}`, resolutionNote)
	const key = "durable-resolution"

	ctx := context.Background()
	outbox := func() []c.Envelope {
		t.Helper()
		events, err := s.Repo.(c.OutboxReader).PendingOutbox(ctx, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		var results []c.Envelope
		for _, event := range events {
			if event.Type == homepageResolveAction {
				results = append(results, event)
			}
		}
		return results
	}
	homepageRejectAudit(t, migration, homepageResolveAction)
	homepageCall(t, h, http.MethodPut, decision, homepageAdmin, key, body, http.StatusServiceUnavailable)
	homepageAssertPending(t, homepageCall(t, h, http.MethodGet, requestURL, homepageAdmin, "", "", http.StatusOK))
	profile := homepagePublicProfile(t, h, homepagePublic+"?reveal_contacts=true")
	if profile == nil || profile.Email != "auto@example.org" || len(outbox()) != 0 {
		t.Fatal("audit failure committed a profile edit or notification")
	}
	homepageAllowAudit(t, migration)

	const callers = 6
	var results [callers]*httptest.ResponseRecorder
	var pending sync.WaitGroup
	for i := range results {
		pending.Go(func() {
			results[i] = homepageRequest(h, http.MethodPut, decision, homepageAdmin, key, body)
		})
	}
	pending.Wait()
	for _, result := range results {
		if result.Code != http.StatusOK || !bytes.Equal(result.Body.Bytes(), results[0].Body.Bytes()) {
			t.Fatalf("concurrent decision did not replay the same receipt: %d %s", result.Code, result.Body.String())
		}
	}
	processed := homepageDecode[homepageRequestReceipt](t, results[0])
	if processed.Status != "approved" || processed.Revision != 2 || homepageAuditCount(t, migration, homepageResolveAction, homepageAdmin) != 1 {
		t.Fatalf("idempotent approval has the wrong terminal state or duplicated audit: %+v", processed)
	}
	events := outbox()
	if len(events) != 1 || events[0].Subject != "teacher-homepage-request/"+submitted.ID || events[0].Data.Revision != processed.Revision {
		t.Fatalf("expected one durable notification event for the processed request: %+v", events)
	}
	payload, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{homepageProposedEmail, homepagePrivateNote, resolutionNote, homepageOwner} {
		if bytes.Contains(payload, []byte(private)) {
			t.Fatal("notification event contains request payload or account identity")
		}
	}
	if repeated := outbox(); len(repeated) != 1 || repeated[0].ID != events[0].ID {
		t.Fatalf("an unconfirmed event was lost or changed on retry: %+v", repeated)
	}
	if err := s.Repo.(c.OutboxReader).ConfirmOutbox(ctx, events[0].ID); err != nil {
		t.Fatal(err)
	}
	homepageCall(t, h, http.MethodPut, decision, homepageAdmin, key, body, http.StatusOK)
	if len(outbox()) != 0 {
		t.Fatal("decision replay requeued a confirmed notification")
	}
}
