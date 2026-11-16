package catalog_test

import (
	"context"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyEnrollmentHTTPShapeAndExplicitTermOrder(t *testing.T) {
	s, p, in := fixture(t)
	if _, err := s.Import(context.Background(), p, "compat", in); err != nil {
		t.Fatal(err)
	}
	h := httpapi.Handler{Service: s, Authenticate: func(*http.Request) (c.Principal, error) { return c.Principal{Subject: "issuer:student"}, nil }}
	for _, query := range []string{"", "?include_term_order=false", "?include_term_order=true", "?include_term_order=yes", "?include_term_order=true&include_term_order=false"} {
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/catalog/me/enrollments"+query, nil))
		if query == "?include_term_order=yes" || query == "?include_term_order=true&include_term_order=false" {
			if rec.Code != 400 {
				t.Fatal(query, rec.Code)
			}
			continue
		}
		var page []c.Enrollment
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page) == 0 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		for _, en := range page {
			if (en.TermOrderKey != nil) != (query == "?include_term_order=true") {
				t.Fatal("legacy enrollment shape", query, en)
			}
		}
		if query == "" || query == "?include_term_order=true" {
			if dir := os.Getenv("STUHELPER_B2_RESPONSE_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, map[bool]string{true: "catalog-enrollments-expanded.json", false: "catalog-enrollments-legacy.json"}[query != ""]), rec.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
