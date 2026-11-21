package catalog_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
)

func TestTeacherHomepageDetailsAndPhotoRequireCurrentAdmission(t *testing.T) {
	s, p, in := teacherDirectoryFixture(t)
	ctx := context.Background()
	candidateRows, err := s.TeacherCandidates(ctx, p, "school", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded.Bytes())
	photoID := hex.EncodeToString(sum[:])
	in.Matches = in.Matches[:1]
	match := in.Matches[0]
	siteID := c.TeacherSiteID(in.SchoolID, "synthetic-site")
	in.Sites = []c.TeacherSite{
		{
			SiteID: siteID, Name: match.Name, Unit: "Synthetic Dept", URL: match.URL,
			CandidateTeacherIDs: []string{match.TeacherID}, CandidateCount: 1,
			Profile: c.TeacherHomepage{
				Name: match.Name, Email: "teacher@example.org", Phone: "123-4567",
				OfficeLocation: "Synthetic Room", HireDate: "2020",
				Fields:   []c.TeacherHomepageField{{Label: "学术兼职", Value: "合成资料"}},
				Sections: []c.TeacherHomepageSection{{Title: "研究成果", Text: "<script>纯文本</script>"}},
			},
			Photo: &c.TeacherHomepageImage{ID: photoID, MediaType: "image/png", Width: 2, Height: 3, Bytes: encoded.Bytes()},
		},
	}
	for _, row := range candidateRows {
		review := c.TeacherSiteTeacher{TeacherID: row.ID, Reason: "name_absent"}
		if row.ID == match.TeacherID {
			review.Reason = ""
			review.CandidateSiteIDs = []string{siteID}
			review.CandidateCount = 1
		}
		in.Teachers = append(in.Teachers, review)
	}
	if _, err := s.ImportTeacherSites(ctx, p, in); err != nil {
		t.Fatal(err)
	}

	access := &readAccess{}
	s.ReadAccess = access
	handler := httpapi.Handler{Service: s, Authenticate: func(r *http.Request) (c.Principal, error) {
		return c.Principal{Subject: r.Header.Get("Authorization")}, nil
	}}.Routes()
	call := func(path, actor string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", actor)
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s returned status %d; want %d", path, w.Code, status)
		}
		return w
	}
	const objectBase = "/api/v1/catalog/objects"
	const opts = "?include_teacher_homepage=true"
	const verified = "issuer:teacher"
	detail := objectBase + "/" + match.TeacherID
	photoURL := detail + "/teacher-homepage/photos/" + photoID
	detailBody := call(detail+opts, verified, http.StatusOK)
	var teacher c.Object
	if err := json.Unmarshal(detailBody.Body.Bytes(), &teacher); err != nil {
		t.Fatal(err)
	}
	home := teacher.TeacherHomepage
	if home == nil || home.Email != in.Sites[0].Profile.Email || home.Phone != in.Sites[0].Profile.Phone ||
		home.OfficeLocation != in.Sites[0].Profile.OfficeLocation || home.HireDate != "2020" ||
		home.Photo == nil || home.Photo.URL != photoURL || home.Photo.Width != 2 || home.Photo.Height != 3 {
		t.Fatal("matched profile or photo metadata missing")
	}
	if teacher.DepartmentID != match.DepartmentID || strings.Contains(detailBody.Body.String(), "shi.buaa.edu.cn") ||
		strings.Contains(detailBody.Body.String(), "official_updated") {
		t.Fatal("homepage replaced directory authority or exposed source metadata")
	}
	if len(home.Fields) != 1 || home.Fields[0].Label != "学术兼职" ||
		len(home.Sections) != 1 || home.Sections[0].Text != "<script>纯文本</script>" {
		t.Fatal("ordered website fields and sections were not returned as text")
	}
	forged := c.Import{SchoolID: in.SchoolID, SourceID: "approved-source", Revision: 2, FetchedAt: time.Now(),
		Objects: []c.Object{{
			ID: match.TeacherID, Name: match.Name, Kind: "teacher", SchoolID: in.SchoolID,
			TeacherHomepage: &c.TeacherHomepage{Name: "Forged"},
		}},
	}
	if _, err := s.Import(ctx, p, "forged-homepage", forged); !errors.Is(err, c.ErrInput) {
		t.Fatal("normal import accepted a read-only homepage field", err)
	}
	summaryBody := call(detail+"/summary"+opts, verified, http.StatusOK)
	var summary c.PublicSummary
	if err := json.Unmarshal(summaryBody.Body.Bytes(), &summary); err != nil || summary.Object.TeacherHomepage == nil {
		t.Fatal("teacher summary did not include profile", err)
	}
	for _, endpoint := range []string{
		detail, detail + "/summary", objectBase + "/teacher" + opts,
		objectBase + "?kind=teacher&include_teacher_homepage=true", objectBase + "/lookup?ids=" + match.TeacherID,
	} {
		body := call(endpoint, verified, http.StatusOK)
		if strings.Contains(body.Body.String(), `"teacher_homepage"`) {
			t.Fatalf("%s exposed optional homepage", endpoint)
		}
	}
	photo := call(photoURL, verified, http.StatusOK)
	if !bytes.Equal(photo.Body.Bytes(), encoded.Bytes()) || photo.Header().Get("Content-Type") != "image/png" ||
		photo.Header().Get("Cache-Control") != "private, no-store" || photo.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("photo content or response headers differ from the contract")
	}
	call(objectBase+"/teacher/teacher-homepage/photos/"+photoID, verified, http.StatusNotFound)
	call(detail+opts+"&include_teacher_homepage=false", verified, http.StatusBadRequest)
	for _, path := range []string{detail + opts, detail + "/summary" + opts, photoURL} {
		call(path, "", http.StatusUnauthorized)
		call(path, "issuer:unverified", http.StatusForbidden)
		access.revoked = true
		call(path, verified, http.StatusForbidden)
		access.revoked = false
	}
}

func TestTeacherSiteReviewHTTPRequiresSchoolPermissions(t *testing.T) {
	s, p, in := teacherDirectoryFixture(t)
	in.Matches = nil
	for _, slug := range []string{"synthetic-alpha", "synthetic-beta"} {
		in.Sites = append(in.Sites, c.TeacherSite{
			SiteID: c.TeacherSiteID(in.SchoolID, slug), Name: slug, Unit: "Synthetic Dept",
			URL: "https://shi.buaa.edu.cn/" + slug + "/zh_CN/index.htm", Reason: "catalog_name_absent",
			Profile: c.TeacherHomepage{Name: slug},
		})
	}
	rows, err := s.TeacherCandidates(context.Background(), p, in.SchoolID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	in.Teachers = make([]c.TeacherSiteTeacher, len(rows))
	for i, row := range rows {
		in.Teachers[i] = c.TeacherSiteTeacher{TeacherID: row.ID, Reason: "name_absent"}
	}
	if _, err := s.ImportTeacherSites(context.Background(), p, in); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.Handler{Service: s, Authenticate: func(r *http.Request) (c.Principal, error) {
		switch r.Header.Get("Authorization") {
		case "reader":
			return c.Principal{Subject: "issuer:reader", Schools: []string{"school"}, CanReadTeacherSites: true}, nil
		case "outsider":
			return c.Principal{Subject: "issuer:outsider", Schools: []string{"other"}, CanReadTeacherSites: true, CanMaintain: true}, nil
		case "maintainer":
			return c.Principal{Subject: "issuer:maintainer", Schools: []string{"school"}, CanMaintain: true}, nil
		default:
			return c.Principal{}, nil
		}
	}}.Routes()
	call := func(method, path, actor, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", actor)
		r.Header.Set("Idempotency-Key", "synthetic-decision")
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s returned status %d; want %d", method, path, w.Code, status)
		}
		return w
	}
	const root = "/api/v1/catalog/admin/schools/school/teacher-sites/review"
	for _, endpoint := range []string{root + "/sites", root + "/teachers"} {
		call(http.MethodGet, endpoint, "", "", http.StatusUnauthorized)
		call(http.MethodGet, endpoint, "outsider", "", http.StatusForbidden)
		call(http.MethodGet, endpoint, "maintainer", "", http.StatusForbidden)
		call(http.MethodGet, endpoint, "reader", "", http.StatusOK)
	}
	first := call(http.MethodGet, root+"/sites?limit=1", "reader", "", http.StatusOK)
	var page c.TeacherSiteReviewPage
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Total != 2 || page.Next == "" {
		t.Fatal("invalid first review page", err)
	}
	next := root + "/sites?limit=1&after=" + url.QueryEscape(page.Next)
	call(http.MethodGet, next+"&status=decided", "reader", "", http.StatusBadRequest)
	call(http.MethodGet, next, "reader", "", http.StatusOK)
	decision := root + "/sites/" + page.Items[0].SiteID + "/decision"
	body := `{"action":"reject","expected_revision":0}`
	call(http.MethodPut, decision, "reader", body, http.StatusForbidden)
	call(http.MethodPut, decision, "outsider", body, http.StatusForbidden)
	call(http.MethodPut, decision, "maintainer", `{"action":"reject"}`, http.StatusBadRequest)
	call(http.MethodPut, decision, "maintainer", body, http.StatusOK)
	call(http.MethodGet, next, "reader", "", http.StatusConflict)
}
