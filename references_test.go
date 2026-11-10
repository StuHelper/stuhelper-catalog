package catalog_test

import (
	"context"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnEnrollmentNamesSurviveAdmissionExpiryWithoutPublicDetails(t *testing.T) {
	s, admin, in := fixture(t)
	in.Enrollments[0].Active = false
	for i := range in.Objects {
		if in.Objects[i].Kind == "teacher" {
			in.Objects[i].Biography = "Restricted biography"
		}
	}
	if _, e := s.Import(context.Background(), admin, "historical", in); e != nil {
		t.Fatal(e)
	}
	s.ReadAccess = &readAccess{revoked: true, unavailable: true}
	h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
		return c.Principal{Subject: req.Header.Get("Authorization")}, nil
	}}.Routes()
	call := func(path, subject string, status int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", subject)
		req.Header.Set("X-User-ID", "issuer:student")
		h.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	path := "/api/v1/catalog/me/references?ids=course,teacher,term1,school"
	call(path, "", 401)
	call(path, "issuer:other", 404)
	body := call(path, "issuer:student", 200)
	var names c.References
	if e := json.Unmarshal(body, &names); e != nil || len(names.Items) != 4 {
		t.Fatal(string(body), e)
	}
	for _, field := range []string{"biography", "credits", "teacher_count", "source_id", "Restricted biography", "time_text"} {
		if strings.Contains(string(body), field) {
			t.Fatal("name endpoint exposed public details", field)
		}
	}
	call("/api/v1/catalog/me/references?ids=course,dept", "issuer:student", 404)
	call("/api/v1/catalog/me/references?ids="+strings.Repeat("course,", 100)+"course", "issuer:student", 400)
	call("/api/v1/catalog/objects/course", "issuer:student", 503)
}
