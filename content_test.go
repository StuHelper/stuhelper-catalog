package catalog_test

import (
	"context"
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type readAccess struct{ revoked, unavailable bool }

func (a *readAccess) Scope(_ context.Context, p c.Principal, school string) (string, error) {
	if a.unavailable {
		return "", c.ErrUnavailable
	}
	if a.revoked || school != "" && school != "school" || p.Subject != "issuer:student" && p.Subject != "issuer:teacher" {
		return "", c.ErrForbidden
	}
	return "school", nil
}
func TestContentReadAdmissionCoversEveryHTTPReadAndPreservesOwnEnrollment(t *testing.T) {
	s, admin, in := fixture(t)
	if _, e := s.Import(context.Background(), admin, "seed", in); e != nil {
		t.Fatal(e)
	}
	access := &readAccess{}
	s.ReadAccess = access
	h := httpapi.Handler{Service: s, Authenticate: func(req *http.Request) (c.Principal, error) {
		switch req.Header.Get("Authorization") {
		case "Bearer student":
			return c.Principal{Subject: "issuer:student"}, nil
		case "Bearer teacher":
			return c.Principal{Subject: "issuer:teacher"}, nil
		case "Bearer admin":
			return admin, nil
		case "Bearer unverified":
			return c.Principal{Subject: "issuer:unverified"}, nil
		default:
			return c.Principal{}, nil
		}
	}}.Routes()
	call := func(path, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", token)
		r.Header.Set("X-User-ID", "issuer:admin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
		if want != 200 && (strings.Contains(w.Body.String(), "items") || strings.Contains(w.Body.String(), "teacher_count") || strings.Contains(w.Body.String(), "credits")) {
			t.Fatal("denied request returned content")
		}
		return w
	}
	paths := []string{"/api/v1/catalog/objects?kind=course", "/api/v1/catalog/objects?kind=course&q=no-match", "/api/v1/catalog/objects/course", "/api/v1/catalog/objects/lookup?ids=course,teacher", "/api/v1/catalog/objects/course/summary", "/api/v1/catalog/objects/course/resolution"}
	for _, path := range paths {
		call(path, "", 401)
		call(path, "Bearer unverified", 403)
		call(path, "Bearer teacher", 200)
		call(path, "Bearer student", 200)
	}
	call("/api/v1/catalog/objects?kind=course&school_id=other", "Bearer student", 403)
	access.revoked = true
	for _, path := range paths {
		call(path, "Bearer student", 403)
	}
	own := call("/api/v1/catalog/me/enrollments?school_id=school&subject=issuer:other", "Bearer student", 200)
	if !strings.Contains(own.Body.String(), "enrollment") {
		t.Fatal("own enrollment blocked after admission expired")
	}
	call("/api/v1/catalog/admin/objects?kind=course&school_id=school", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course/summary", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/lookup?ids=course", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course/resolution", "Bearer admin", 200)
	call("/api/v1/catalog/admin/objects/course", "Bearer student", 403)
	call("/api/v1/catalog/admin/objects?kind=course&school_id=other", "Bearer admin", 403)
	access.unavailable = true
	for _, path := range paths {
		call(path, "Bearer student", 503)
	}
	call("/api/v1/catalog/admin/objects/course", "Bearer admin", 200)
	s.ReadAccess = nil
	if _, e := s.Read(context.Background(), c.Principal{Subject: "issuer:student"}, "course"); !errors.Is(e, c.ErrUnavailable) {
		t.Fatal("missing admission provider allowed read", e)
	}
}
