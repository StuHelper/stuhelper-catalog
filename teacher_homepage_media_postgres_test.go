package catalog_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
)

type bodyMediaReadAccess struct{ readAccess }

func (a *bodyMediaReadAccess) Scope(ctx context.Context, p c.Principal, school string) (string, error) {
	if p.Subject == "issuer:verified-other" {
		return "other", nil
	}
	return a.readAccess.Scope(ctx, p, school)
}

func TestTeacherHomepageBodyMediaPostgresAuthorizationAndRevocation(t *testing.T) {
	checkBodyMediaStorage(t, nil)
}

func checkBodyMediaStorage(t *testing.T, store c.TeacherImageStorage) {
	t.Helper()
	s, p, _, in := homepageFixtureWithImages(t, store)
	access := &bodyMediaReadAccess{}
	s.ReadAccess = access
	h := homepageHandler(s)
	data := homepagePortrait(t)
	sha := sha256.Sum256(data)
	imageID := hex.EncodeToString(sha[:])
	image := &c.TeacherHomepageBodyImage{ID: imageID, MediaType: "image/png", Width: 3, Height: 1, SizeBytes: int64(len(data))}
	const mediaURL = homepagePublic + "/media"
	const originalArticle = "https://shi.buaa.edu.cn/auto/en/publications/200.htm"
	const fallbackPage = "https://shi.buaa.edu.cn/auto/en/index.htm"
	const videoURL = "https://videos.example.org/course/123"
	const paperURL = "https://publisher.example.org/papers/example.pdf?fmt=paper#page=2"
	const privateURL = "https://publisher.example.org/files/auto%40example.org/"
	imageURL := homepagePublic + "/images/" + imageID
	in = homepageNextSnapshot(in)
	in.Version = 3
	in.ReadBodyImage = func(_ context.Context, id string) ([]byte, error) {
		if id != imageID {
			return nil, fmt.Errorf("unexpected digest")
		}
		return data, nil
	}
	in.Sites[0].BodyMedia = []c.TeacherHomepageBodyMedia{
		{Kind: "image", Field: "biography", Title: "Original figure", LinkURL: fallbackPage},
		{Kind: "image", Field: "sections", Section: "Publications", Title: "Current figure", LinkURL: originalArticle, Image: image},
		{Kind: "video", Field: "sections", Section: "Teaching", Title: "Media-only section", LinkURL: videoURL},
		{Kind: "attachment", Field: "research", Title: "Open publication", LinkURL: paperURL},
		{Kind: "attachment", Field: "research", Title: "Publication with contact path", LinkURL: privateURL},
		{Kind: "attachment", Field: "sections", Section: "Unapproved category", Title: "Section restricted by policy", LinkURL: paperURL},
	}
	publishTeacherHomepagePostgresSnapshot(t, s, p, "media-before-sharing", in)
	readMedia := func() []c.TeacherHomepageMedium {
		t.Helper()
		out := homepageCall(t, h, http.MethodGet, mediaURL, homepageReader, "", "", http.StatusOK)
		return homepageDecode[c.TeacherHomepageMediaView](t, out).Items
	}
	checkImage := func(status int) {
		t.Helper()
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := homepageCall(t, h, method, imageURL, homepageReader, "", "", status)
			if status != http.StatusOK {
				continue
			}
			if r.Header().Get("Content-Type") != "image/png" || r.Header().Get("Content-Length") != strconv.Itoa(len(data)) ||
				r.Header().Get("X-Content-Type-Options") != "nosniff" || r.Header().Get("X-Accel-Buffering") != "no" {
				t.Fatalf("invalid image headers: %v", r.Header())
			}
			if method == http.MethodHead && r.Body.Len() != 0 || method == http.MethodGet && !bytes.Equal(r.Body.Bytes(), data) {
				t.Fatalf("%s produced %d bytes", method, r.Body.Len())
			}
		}
	}
	items := readMedia()
	if len(items) != 5 || items[0].URL != fallbackPage || items[0].Image != nil || items[1].URL != imageURL || items[1].Image == nil ||
		items[1].Image.SizeBytes != int64(len(data)) || items[2].Section != "Teaching" || items[2].URL != videoURL || items[2].Image != nil ||
		items[3].Kind != "attachment" || items[3].URL != paperURL || items[3].Image != nil || items[4].URL != privateURL {
		t.Fatalf("wrong ordered media and fallback view: %+v", items)
	}
	checkImage(http.StatusOK)
	if metadata, err := s.ReadTeacherHomepageBodyImage(context.Background(), p, "official", imageID, false); err != nil ||
		metadata.SizeBytes != int64(len(data)) || len(metadata.Bytes) != 0 {
		t.Fatalf("metadata-only read loaded body or returned no size: %+v %v", metadata, err)
	}
	for _, route := range []string{homepagePublic, homepageObject + "?include_teacher_homepage=true", homepageObject + "/summary?include_teacher_homepage=true"} {
		r := homepageCall(t, h, http.MethodGet, route, homepageReader, "", "", http.StatusOK)
		if strings.Contains(r.Body.String(), "body_media") || strings.Contains(r.Body.String(), originalArticle) || strings.Contains(r.Body.String(), imageURL) {
			t.Fatal("legacy shape leaked body media")
		}
	}
	for _, endpoint := range []string{mediaURL, imageURL} {
		homepageCall(t, h, http.MethodGet, endpoint, "", "", "", http.StatusUnauthorized)
		homepageCall(t, h, http.MethodGet, endpoint, "issuer:unverified", "", "", http.StatusForbidden)
		homepageCall(t, h, http.MethodGet, endpoint, "issuer:verified-other", "", "", http.StatusForbidden)
	}
	homepageCall(t, h, http.MethodHead, imageURL, "issuer:verified-other", "", "", http.StatusForbidden)
	homepageCall(t, h, http.MethodGet, strings.Replace(imageURL, "/official/", "/teacher/", 1), homepageReader, "", "", http.StatusNotFound)

	const hideColumn = `[{"field":{"field":"sections","label":"Publications"},"hidden":true},{"field":{"field":"email"},"hidden":true}]`
	revision := homepageSet(t, h, 0, "visible", hideColumn, "hide-section-and-contact")
	if items := readMedia(); len(items) != 3 || items[0].URL != fallbackPage || items[1].URL != videoURL || items[2].URL != paperURL {
		t.Fatalf("hidden source or hidden contact in a link survived: %+v", items)
	}
	checkImage(http.StatusNotFound)
	const overrideColumn = `[{"field":{"field":"sections","label":"Publications"},"hidden":false,"override":{"text":"Edited publication notes"}}]`
	revision = homepageSet(t, h, revision, "visible", overrideColumn, "replace-section")
	checkImage(http.StatusNotFound)
	const overrideAllSections = `[{"field":{"field":"sections"},"hidden":false,"override":{"sections":[{"title":"Publications","text":"Manually replaced source sections"}]}}]`
	revision = homepageSet(t, h, revision, "visible", overrideAllSections, "replace-section-group")
	checkImage(http.StatusNotFound)
	for _, item := range readMedia() {
		if item.Field == "sections" {
			t.Fatalf("section group override retained source media: %+v", item)
		}
	}
	revision = homepageSet(t, h, revision, "visible", `[]`, "restore-section")
	checkImage(http.StatusOK)
	allowlist := homepageDecode[c.TeacherHomepageAllowlist](t, homepageCall(t, h, http.MethodGet, homepageAllowlist, homepageAdmin, "", "", http.StatusOK))
	policy := fmt.Sprintf(`{"expected_revision":%d,"fields":[{"field":"research"}]}`, allowlist.Revision)
	r := homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "withdraw-school-section", policy, http.StatusOK)
	if items := readMedia(); len(items) != 1 || items[0].URL != paperURL {
		t.Fatalf("school policy left withdrawn media visible: %+v", items)
	}
	checkImage(http.StatusNotFound)
	allowlist = homepageDecode[c.TeacherHomepageAllowlist](t, r)
	policy = fmt.Sprintf(`{"expected_revision":%d,"fields":[{"field":"sections","label":"Publications"}]}`, allowlist.Revision)
	homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "restore-school-section", policy, http.StatusOK)
	checkImage(http.StatusOK)
	revision = homepageSet(t, h, revision, "stopped", `[]`, "stop-body-media")
	if items := readMedia(); items == nil || len(items) != 0 {
		t.Fatalf("stopped page media should be an empty array: %+v", items)
	}
	checkImage(http.StatusNotFound)
	homepageSet(t, h, revision, "visible", `[]`, "restore-body-media")
	checkImage(http.StatusOK)
	access.revoked = true
	for _, endpoint := range []string{mediaURL, imageURL} {
		homepageCall(t, h, http.MethodGet, endpoint, homepageReader, "", "", http.StatusForbidden)
	}
	access.revoked = false

	removed := homepageNextSnapshot(in)
	removed.Sites = append([]c.TeacherSite(nil), in.Sites...)
	removed.Sites[0].BodyMedia = nil
	removed.ReadBodyImage = nil
	removed.Version = 2
	publishTeacherHomepagePostgresSnapshot(t, s, p, "older-format-published-without-media", removed)
	if items := readMedia(); len(items) != 0 {
		t.Fatalf("removed media still in list: %+v", items)
	}
	checkImage(http.StatusNotFound)

	in = homepageNextSnapshot(removed)
	in.Version = 3
	in.Sites[0].BodyMedia = []c.TeacherHomepageBodyMedia{
		{Kind: "image", Field: "sections", Section: "Publications", Title: "Restored figure", LinkURL: originalArticle, Image: image},
	}
	in.ReadBodyImage = func(context.Context, string) ([]byte, error) { return data, nil }
	publishTeacherHomepagePostgresSnapshot(t, s, p, "matching-restores-media", in)
	checkImage(http.StatusOK)
	in = homepageNextSnapshot(in)
	in.Matches = nil
	in.Sites[0].Reason = "unit_mismatch"
	in.Teachers[0].Reason = "unit_mismatch"
	publishTeacherHomepagePostgresSnapshot(t, s, p, "without-teacher-match", in)
	homepageCall(t, h, http.MethodGet, mediaURL, homepageReader, "", "", http.StatusNotFound)
	checkImage(http.StatusNotFound)
}
