package catalog_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
)

const (
	homepageAdminBase = "/api/v1/catalog/admin/schools/school/teacher-sites"
	homepageObject    = "/api/v1/catalog/objects/official"
	homepagePublic    = homepageObject + "/teacher-homepage"
	homepageControls  = homepageAdminBase + "/teachers/official/homepage"
	homepageAllowlist = homepageAdminBase + "/allowlist"
	homepageReader    = "issuer:student"
	homepageOwner     = "issuer:teacher"
	homepageAdmin     = "issuer:homepage-admin"
)

func homepageHandler(s *c.Service) http.Handler {
	return httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
		p := c.Principal{Subject: req.Header.Get("Authorization")}
		switch p.Subject {
		case homepageAdmin:
			p.Schools = []string{"school"}
			p.CanMaintain, p.CanReadTeacherSites = true, true
		case "issuer:school-reader":
			p.Schools = []string{"school"}
			p.CanReadTeacherSites = true
		case "issuer:other-admin":
			p.Schools = []string{"other"}
			p.CanMaintain, p.CanReadTeacherSites = true, true
		}
		return p, nil
	}}.Routes()
}

func homepageRequest(h http.Handler, method, path, subject, key, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", subject)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	h.ServeHTTP(recorder, req)
	return recorder
}

func homepageCall(t *testing.T, h http.Handler, method, path, subject, key, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	out := homepageRequest(h, method, path, subject, key, body)
	if out.Code != status {
		t.Fatalf("%s %s returned %d; want %d: %s", method, path, out.Code, status, out.Body.String())
	}
	if !strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%s %s may be cached: %v", method, path, out.Header())
	}
	return out
}

func homepageDecode[T any](t *testing.T, out *httptest.ResponseRecorder) T {
	t.Helper()
	var decoded T
	if err := json.Unmarshal(out.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return decoded
}

func homepageFixture(t *testing.T) (*c.Service, c.Principal, *sql.DB, c.TeacherSiteSnapshot) {
	t.Helper()
	return homepageFixtureWithImages(t, nil)
}

func homepageFixtureWithImages(t *testing.T, storage c.TeacherImageStorage) (*c.Service, c.Principal, *sql.DB, c.TeacherSiteSnapshot) {
	t.Helper()
	s, p, migration, _ := projectionFixture(t)
	s.Repo.(*postgres.Repository).TeacherImageStorage = storage
	p.CanRefreshTeacherSites = true
	in := teacherHomepagePostgresSnapshot(t, s, p)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "homepage-controls-initial", in)
	return s, p, migration, in
}

func homepageNextSnapshot(in c.TeacherSiteSnapshot) c.TeacherSiteSnapshot {
	in.ObservedAt = in.ObservedAt.Add(time.Microsecond)
	in.Revision = in.ObservedAt.UnixMicro()
	return in
}

func homepageRevision(t *testing.T, out *httptest.ResponseRecorder) int64 {
	t.Helper()
	state := homepageDecode[struct {
		Revision *int64 `json:"revision"`
	}](t, out)
	if state.Revision == nil || *state.Revision < 1 {
		t.Fatalf("missing persisted revision: %s", out.Body.String())
	}
	return *state.Revision
}

func homepageSet(t *testing.T, h http.Handler, revision int64, visibility, rules, key string) int64 {
	t.Helper()
	body := fmt.Sprintf(`{"expected_revision":%d,"page_visibility":%q,"rules":%s}`, revision, visibility, rules)
	out := homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, key, body, http.StatusOK)
	return homepageRevision(t, out)
}

func homepagePublicProfile(t *testing.T, h http.Handler, path string) *c.TeacherHomepage {
	t.Helper()
	response := homepageCall(t, h, http.MethodGet, path, homepageReader, "", "", http.StatusOK)
	return homepageDecode[struct {
		Profile *c.TeacherHomepage `json:"profile"`
	}](t, response).Profile
}

func TestTeacherHomepageControlsPostgresVisibilityAndRefresh(t *testing.T) {
	s, p, _, source := homepageFixture(t)
	h := homepageHandler(s)
	photo := source.Sites[0].Photo
	photoURL := homepageObject + "/teacher-homepage/photos/" + photo.ID
	original := homepageCall(t, h, http.MethodGet, photoURL, homepageReader, "", "", http.StatusOK)
	if !bytes.Equal(photo.Bytes, original.Body.Bytes()) || original.Header().Get("Content-Type") != photo.MediaType {
		t.Fatal("original avatar did not match the source")
	}
	profile := func() *c.TeacherHomepage {
		t.Helper()
		detail := homepageCall(t, h, http.MethodGet, homepageObject+"?include_teacher_homepage=true", homepageReader, "", "", http.StatusOK)
		object := homepageDecode[c.Object](t, detail)
		summary := homepageDecode[c.PublicSummary](t, homepageCall(t, h, http.MethodGet, homepageObject+"/summary?include_teacher_homepage=true", homepageReader, "", "", http.StatusOK))
		if object.ID != "official" || object.Name != "Official" || object.DepartmentID != "dept" {
			t.Fatalf("homepage settings changed the directory object: %+v", object)
		}
		detailHome, _ := json.Marshal(object.TeacherHomepage)
		summaryHome, _ := json.Marshal(summary.Object.TeacherHomepage)
		if !bytes.Equal(detailHome, summaryHome) {
			t.Fatal("detail and summary disagree about homepage visibility")
		}
		publicHome, _ := json.Marshal(homepagePublicProfile(t, h, homepagePublic))
		if !bytes.Equal(publicHome, detailHome) {
			t.Fatal("dedicated homepage and teacher detail disagree about homepage visibility")
		}
		return object.TeacherHomepage
	}
	const visibleRules = `[{"field":{"field":"email"},"hidden":false,"override":{"text":"approved@example.org"}},{"field":{"field":"phone"},"hidden":true},{"field":{"field":"photo"},"hidden":true}]`
	revision := homepageSet(t, h, 0, "visible", visibleRules, "hide-phone-and-photo")
	if published := profile(); published == nil || published.Email != "approved@example.org" || published.Phone != "" || published.Photo != nil {
		t.Fatalf("field controls were not applied: %+v", published)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		homepageCall(t, h, method, photoURL, homepageReader, "", "", http.StatusNotFound)
	}
	source = homepageNextSnapshot(source)
	source.Sites[0].Profile.Email = "updated@example.org"
	source.Sites[0].Profile.Phone = "555-0199"
	publishTeacherHomepagePostgresSnapshot(t, s, p, "refresh-under-manual-controls", source)
	if published := profile(); published == nil || published.Email != "approved@example.org" || published.Phone != "" || published.Photo != nil {
		t.Fatalf("crawl displaced an override or hide rule: %+v", published)
	}
	admin := homepageCall(t, h, http.MethodGet, homepageControls, homepageAdmin, "", "", http.StatusOK)
	if !strings.Contains(admin.Body.String(), "updated@example.org") || !strings.Contains(admin.Body.String(), "approved@example.org") {
		t.Fatal("admin payload does not distinguish source and override")
	}
	revision = homepageSet(t, h, revision, "hidden", visibleRules, "hide-profile")
	if published := profile(); published != nil {
		t.Fatal("page hiding exposed homepage fields")
	}
	homepageCall(t, h, http.MethodGet, photoURL, homepageReader, "", "", http.StatusNotFound)
	revision = homepageSet(t, h, revision, "stopped", visibleRules, "stop-profile")
	source = homepageNextSnapshot(source)
	publishTeacherHomepagePostgresSnapshot(t, s, p, "refresh-stopped-profile", source)
	if published := profile(); published != nil {
		t.Fatal("a successful crawl resumed a stopped homepage")
	}
	homepageCall(t, h, http.MethodHead, photoURL, homepageReader, "", "", http.StatusNotFound)

	revision = homepageSet(t, h, revision, "visible", "[]", "restore-profile")
	if published := profile(); published == nil || published.Email != "updated@example.org" || published.Phone != "555-0199" ||
		published.Photo == nil || published.Photo.URL != photoURL {
		t.Fatalf("clear and resume did not restore the latest source: %+v", published)
	}
	homepageCall(t, h, http.MethodHead, photoURL, homepageReader, "", "", http.StatusOK)
	list := homepageCall(t, h, http.MethodGet, "/api/v1/catalog/objects?kind=teacher", homepageReader, "", "", http.StatusOK)
	if strings.Contains(list.Body.String(), "updated@example.org") || strings.Contains(list.Body.String(), "teacher_homepage") {
		t.Fatal("homepage data leaked into the directory list")
	}
	homepageSet(t, h, revision, "visible", `[{"field":{"field":"name"},"hidden":true}]`, "hide-site-name")
	nameHidden := homepageCall(t, h, http.MethodGet, homepagePublic, homepageReader, "", "", http.StatusOK)
	payload := homepageDecode[struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}](t, nameHidden)
	if _, exposesName := payload.Profile["name"]; exposesName || payload.Profile["email"] == nil {
		t.Fatal("single-field name hide did not preserve the rest of the profile")
	}
	for _, path := range []string{homepageObject + "?include_teacher_homepage=true", homepageObject + "/summary?include_teacher_homepage=true"} {
		legacy := homepageCall(t, h, http.MethodGet, path, homepageReader, "", "", http.StatusOK)
		if strings.Contains(legacy.Body.String(), "teacher_homepage") {
			t.Fatal("legacy response carries a profile incompatible with its required name")
		}
	}
	homepageCall(t, h, http.MethodGet, homepagePublic, "", "", "", http.StatusUnauthorized)
	homepageCall(t, h, http.MethodGet, homepagePublic, "issuer:unverified", "", "", http.StatusForbidden)
}

func homepagePortrait(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 3, 1))
	picture.Set(2, 0, color.RGBA{B: 210, A: 255})
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestTeacherHomepageControlsPostgresSchoolAllowlistAndReplacementPhoto(t *testing.T) {
	checkOverridePortraitStorage(t, nil)
}

func checkOverridePortraitStorage(t *testing.T, store c.TeacherImageStorage) {
	t.Helper()
	s, p, _, source := homepageFixtureWithImages(t, store)
	h := homepageHandler(s)
	source = homepageNextSnapshot(source)
	source.Sites[0].Profile.Fields = append(source.Sites[0].Profile.Fields,
		c.TeacherHomepageField{Label: "E-mail", Value: "auto@example.org"},
		c.TeacherHomepageField{Label: "Unreviewed Label", Value: "private-field-from-website"})
	source.Sites[0].Profile.Sections = append(source.Sites[0].Profile.Sections,
		c.TeacherHomepageSection{Title: "Unreviewed Section", Text: "private-section-from-website"})
	publishTeacherHomepagePostgresSnapshot(t, s, p, "source-with-unreviewed-fields", source)

	profile := homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Photo == nil {
		t.Fatal("the initial approved source profile should be visible")
	}
	public, _ := json.Marshal(profile)
	if bytes.Contains(public, []byte("private-field-from-website")) || bytes.Contains(public, []byte("private-section-from-website")) {
		t.Fatal("unknown source fields bypassed the default school allowlist")
	}
	control := homepageCall(t, h, http.MethodGet, homepageControls, homepageAdmin, "", "", http.StatusOK)
	if !strings.Contains(control.Body.String(), "private-field-from-website") || !strings.Contains(control.Body.String(), "private-section-from-website") {
		t.Fatal("source data needed for review was discarded")
	}
	sourcePhotoURL := profile.Photo.URL
	current := homepageDecode[struct {
		Revision int64 `json:"revision"`
	}](t, homepageCall(t, h, http.MethodGet, homepageAllowlist, homepageAdmin, "", "", http.StatusOK))

	const allowedFields = `[{"field":"name"},{"field":"fields","label":"Unreviewed Label"},{"field":"fields","label":"E-mail"},{"field":"sections","label":"Unreviewed Section"}]`
	schoolPolicy := fmt.Sprintf(`{"expected_revision":%d,"fields":%s}`, current.Revision, allowedFields)
	homepageCall(t, h, http.MethodPut, homepageAllowlist, "issuer:other-admin", "cross-school-policy", schoolPolicy, http.StatusForbidden)
	homepageCall(t, h, http.MethodPut, homepageAllowlist, "issuer:school-reader", "read-policy", schoolPolicy, http.StatusForbidden)
	badPolicy := fmt.Sprintf(`{"expected_revision":%d,"fields":[{"field":"fields"}]}`, current.Revision)
	homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "wildcard-policy", badPolicy, http.StatusBadRequest)
	response := homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "school-policy", schoolPolicy, http.StatusOK)
	replay := homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "school-policy", schoolPolicy, http.StatusOK)
	if !bytes.Equal(response.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("school allowlist update did not replay its response")
	}
	homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "stale-policy", schoolPolicy, http.StatusConflict)
	policyRev := homepageRevision(t, response)
	profile = homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Email != "" || profile.Photo != nil || len(profile.Sections) != 1 ||
		profile.Sections[0].Text != "private-section-from-website" {
		t.Fatalf("school allowlist did not control collected fields: %+v", profile)
	}
	homepageCall(t, h, http.MethodGet, sourcePhotoURL, homepageReader, "", "", http.StatusNotFound)

	uploaded := homepagePortrait(t)
	upload, err := json.Marshal(c.TeacherHomepageFieldPhoto{ContentType: "image/png", Data: uploaded})
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly approved manual values can be visible without adding the
	// corresponding website field to the school allowlist.
	rules := fmt.Sprintf(`[{"field":{"field":"email"},"hidden":false,"override":{"text":"edited@example.org"}},{"field":{"field":"photo"},"hidden":false,"override":{"photo":%s}}]`, upload)
	controlsRev := homepageSet(t, h, 0, "visible", rules, "replace-portrait")
	profile = homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Email != "edited@example.org" || profile.Photo == nil || profile.Photo.URL == sourcePhotoURL ||
		profile.Photo.Width != 3 || profile.Photo.Height != 1 {
		t.Fatalf("approved override did not replace collected values: %+v", profile)
	}
	manualPhotoURL := profile.Photo.URL
	manual := homepageCall(t, h, http.MethodGet, manualPhotoURL, homepageReader, "", "", http.StatusOK)
	canonical, err := png.Decode(manual.Body)
	if err != nil || color.RGBAModel.Convert(canonical.At(2, 0)).(color.RGBA).B != 210 {
		t.Fatal("uploaded avatar content was not available over the authorized URL", err)
	}
	homepageCall(t, h, http.MethodHead, sourcePhotoURL, homepageReader, "", "", http.StatusNotFound)

	controlsRev = homepageSet(t, h, controlsRev, "visible", `[{"field":{"field":"email"},"hidden":true}]`, "hide-all-email-copies")
	profile = homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Email != "" || profile.Photo != nil {
		t.Fatalf("hidden or cleared fields remained visible: %+v", profile)
	}
	for _, field := range profile.Fields {
		if strings.Contains(field.Value, "@example.org") {
			t.Fatal("typed field hide failed to remove an allowlisted alias of the same value")
		}
	}
	homepageCall(t, h, http.MethodHead, manualPhotoURL, homepageReader, "", "", http.StatusNotFound)
	reopen := fmt.Sprintf(`{"expected_revision":%d,"fields":[{"field":"name"},{"field":"photo"}]}`, policyRev)
	homepageCall(t, h, http.MethodPut, homepageAllowlist, homepageAdmin, "restore-source-photo-policy", reopen, http.StatusOK)
	homepageSet(t, h, controlsRev, "visible", "[]", "clear-label-hides")
	if profile = homepagePublicProfile(t, h, homepagePublic); profile == nil || profile.Photo == nil || profile.Photo.URL != sourcePhotoURL {
		t.Fatalf("restoring source allowlist did not restore the original portrait: %+v", profile)
	}
	homepageCall(t, h, http.MethodGet, sourcePhotoURL, homepageReader, "", "", http.StatusOK)
}

func homepageAuditCount(t *testing.T, migration *sql.DB, action, actor string) int {
	t.Helper()
	var count int
	err := migration.QueryRowContext(context.Background(), `SELECT count(*) FROM catalog.audit
 WHERE audit_action=$1 AND audit_actor=$2 AND audit_school='school' AND result='success'`, action, actor).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func homepageRejectAudit(t *testing.T, migration *sql.DB, action string) {
	t.Helper()
	_, err := migration.Exec(`CREATE FUNCTION catalog.reject_homepage_test_audit()
 RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
   IF NEW.audit_action=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic homepage audit failure'; END IF;
   RETURN NEW;
 END $$`)
	if err != nil {
		t.Fatal(err)
	}
	// The action is a fixed internal audit name from the implementation.
	if strings.ContainsRune(action, '\'') {
		t.Fatal("invalid test audit action")
	}
	_, err = migration.Exec(`CREATE TRIGGER reject_homepage_test_audit BEFORE INSERT ON catalog.audit
 FOR EACH ROW EXECUTE FUNCTION catalog.reject_homepage_test_audit('` + action + `')`)
	if err != nil {
		t.Fatal(err)
	}
}

func homepageAllowAudit(t *testing.T, migration *sql.DB) {
	t.Helper()
	if _, err := migration.Exec(`DROP TRIGGER reject_homepage_test_audit ON catalog.audit`); err != nil {
		t.Fatal(err)
	}
}

func TestTeacherHomepageControlsPostgresAuditAndReplay(t *testing.T) {
	s, _, migration, _ := homepageFixture(t)
	h := homepageHandler(s)
	const body = `{"expected_revision":0,"page_visibility":"visible","rules":[{"field":{"field":"email"},"hidden":false,"override":{"text":"edited@example.org"}}]}`
	for _, check := range []struct {
		actor  string
		status int
	}{
		{"", http.StatusUnauthorized},
		{homepageReader, http.StatusForbidden},
		{"issuer:school-reader", http.StatusForbidden},
		{"issuer:other-admin", http.StatusForbidden},
	} {
		homepageCall(t, h, http.MethodGet, homepageControls, check.actor, "", "", check.status)
		homepageCall(t, h, http.MethodPut, homepageControls, check.actor, "control-denied", body, check.status)
	}
	homepageCall(t, h, http.MethodGet, homepageAdminBase+"/teachers/course/homepage", homepageAdmin, "", "", http.StatusNotFound)
	homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, "", body, http.StatusBadRequest)
	homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, "bad-field", `{"expected_revision":0,"page_visibility":"visible","rules":[{"field":{"field":"department_id"},"hidden":true}]}`, http.StatusBadRequest)

	const action = "catalog.teacher-sites.homepage-controls-updated.v1"
	const key = "concurrent-homepage-override"
	homepageRejectAudit(t, migration, action)
	homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, key, body, http.StatusServiceUnavailable)
	object := homepageDecode[c.Object](t, homepageCall(t, h, http.MethodGet, homepageObject+"?include_teacher_homepage=true", homepageReader, "", "", http.StatusOK))
	if object.TeacherHomepage == nil || object.TeacherHomepage.Email != "auto@example.org" {
		t.Fatal("audit failure committed an override")
	}
	homepageAllowAudit(t, migration)

	const callers = 6
	var results [callers]*httptest.ResponseRecorder
	var pending sync.WaitGroup
	for i := range results {
		pending.Go(func() {
			results[i] = homepageRequest(h, http.MethodPut, homepageControls, homepageAdmin, key, body)
		})
	}
	pending.Wait()
	for _, result := range results {
		if result.Code != http.StatusOK || !bytes.Equal(result.Body.Bytes(), results[0].Body.Bytes()) {
			t.Fatalf("concurrent duplicate did not return the original receipt: %d %s", result.Code, result.Body.String())
		}
	}
	if count := homepageAuditCount(t, migration, action, homepageAdmin); count != 1 {
		t.Fatalf("concurrent duplicate wrote %d successful audit entries; want 1", count)
	}
	var reason string
	var data []byte
	if err := migration.QueryRow(`SELECT reason,data FROM catalog.audit
 WHERE audit_action=$1 AND audit_actor=$2 AND result='success'`, action, homepageAdmin).Scan(&reason, &data); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Changes struct {
			Fields []struct {
				Field   string   `json:"field"`
				Changes []string `json:"changes"`
			} `json:"fields"`
		} `json:"homepage_changes"`
	}
	if err := json.Unmarshal(data, &audit); err != nil {
		t.Fatal(err)
	}
	fields := audit.Changes.Fields
	if reason != "override_set" || len(fields) != 1 || fields[0].Field != "email" ||
		len(fields[0].Changes) != 1 || fields[0].Changes[0] != "override_set" || bytes.Contains(data, []byte("@example.org")) {
		t.Fatal("audit must identify the overridden field without recording its value")
	}
	if rev := homepageRevision(t, results[0]); rev != 1 {
		t.Fatalf("first committed revision = %d; want 1", rev)
	}
	homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, "unrelated-stale-edit", body, http.StatusConflict)
	changed := strings.Replace(body, "edited@example.org", "different@example.org", 1)
	homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, key, changed, http.StatusConflict)

	object = homepageDecode[c.Object](t, homepageCall(t, h, http.MethodGet, homepageObject+"?include_teacher_homepage=true", homepageReader, "", "", http.StatusOK))
	if object.TeacherHomepage == nil || object.TeacherHomepage.Email != "edited@example.org" {
		t.Fatal("committed value was overwritten")
	}
	homepageSet(t, h, 1, "visible", "[]", "clear-after-replay")
	old := homepageCall(t, h, http.MethodPut, homepageControls, homepageAdmin, key, body, http.StatusOK)
	if homepageRevision(t, old) != 1 || strings.Contains(old.Body.String(), "edited@example.org") {
		t.Fatal("replaying a revoked override exposed old content rather than its receipt")
	}
	if count := homepageAuditCount(t, migration, action, homepageAdmin); count != 2 {
		t.Fatalf("clearing and replaying added %d total successful audit entries; want 2", count)
	}
	if profile := homepagePublicProfile(t, h, homepagePublic); profile == nil || profile.Email != "auto@example.org" {
		t.Fatalf("replaying a revoked override changed the published state: %+v", profile)
	}
}
