package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/httpapi"
	"github.com/StuHelper/stuhelper-catalog/sdk"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestOpenAPINullableSDKAndOptionalBodyPreviewHeaders(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	admin.CanReadTeacherSites = true
	admin.CanRefreshTeacherSites = true
	in.Objects = append(in.Objects,
		c.Object{ID: "dept-merged", Kind: "department", SchoolID: "school", Name: "Old Dept", Inactive: true, Aliases: []string{}},
		c.Object{ID: "course-dept", Kind: "course", SchoolID: "school", Name: "Dept Course", DepartmentID: "dept-merged", Aliases: []string{}},
	)
	if _, err := s.Import(ctx, admin, "openapi-compat", in); err != nil {
		t.Fatal(err)
	}

	routes := httpapi.Handler{Service: s, Authenticate: func(r *http.Request) (c.Principal, error) {
		if r.Header.Get("X-Role") == "admin" {
			return admin, nil
		}
		return c.Principal{Subject: "issuer:student"}, nil
	}}.Routes()

	// A-11(1): PUT /me/recent-courses/{id} with ContentLength=-1 (HTTP/2 or chunked empty stream) must succeed.
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/catalog/me/recent-courses/course", io.NopCloser(bytes.NewReader(nil)))
	putReq.ContentLength = -1
	putRec := httptest.NewRecorder()
	routes.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT recent-courses with ContentLength=-1 returned %d: %s", putRec.Code, putRec.Body.String())
	}

	// Non-empty unknown JSON must still be rejected with 400.
	badPut := httptest.NewRequest(http.MethodPut, "/api/v1/catalog/me/recent-courses/course", strings.NewReader(`{"extra":true}`))
	badPut.ContentLength = -1
	badRec := httptest.NewRecorder()
	routes.ServeHTTP(badRec, badPut)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("PUT recent-courses with extra field returned %d", badRec.Code)
	}

	// A-04(1): TeacherRefreshStatus.latest and TeacherRefreshRun.counts must be nullable in Go & TS SDKs.
	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/admin/schools/school/teacher-sites/refresh", nil)
	statusReq.Header.Set("X-Role", "admin")
	statusRec := httptest.NewRecorder()
	routes.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("refresh status returned %d: %s", statusRec.Code, statusRec.Body.String())
	}
	var status sdk.TeacherRefreshStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(status.Latest).Kind() != reflect.Pointer || !reflect.ValueOf(status.Latest).IsNil() {
		t.Fatalf("expected sdk.TeacherRefreshStatus.Latest to be nil pointer for null JSON, got %T %+v", status.Latest, status.Latest)
	}

	queueReq := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/admin/schools/school/teacher-sites/refresh", strings.NewReader(`{"mode":"incremental"}`))
	queueReq.Header.Set("X-Role", "admin")
	queueReq.Header.Set("Idempotency-Key", "queue-1")
	queueRec := httptest.NewRecorder()
	routes.ServeHTTP(queueRec, queueReq)
	if queueRec.Code != http.StatusAccepted {
		t.Fatalf("queue refresh returned %d: %s", queueRec.Code, queueRec.Body.String())
	}
	var run sdk.TeacherRefreshRun
	if err := json.Unmarshal(queueRec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(run.Counts).Kind() != reflect.Pointer || !reflect.ValueOf(run.Counts).IsNil() {
		t.Fatalf("expected sdk.TeacherRefreshRun.Counts to be nil pointer for null JSON, got %T %+v", run.Counts, run.Counts)
	}

	// A-04(2): Admin summary department with inactive=true must strictly match generated sdk.Summary without unknown fields.
	sumReq := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/admin/objects/course-dept/summary", nil)
	sumReq.Header.Set("X-Role", "admin")
	sumRec := httptest.NewRecorder()
	routes.ServeHTTP(sumRec, sumReq)
	if sumRec.Code != http.StatusOK {
		t.Fatalf("admin summary returned %d: %s", sumRec.Code, sumRec.Body.String())
	}
	dec := json.NewDecoder(bytes.NewReader(sumRec.Body.Bytes()))
	dec.DisallowUnknownFields()
	var sdkSum sdk.Summary
	if err := dec.Decode(&sdkSum); err != nil {
		t.Fatalf("sdk.Summary drifted from Object schema on inactive department: %v\nbody=%s", err, sumRec.Body.String())
	}

	tsBytes, err := os.ReadFile("sdk/catalog.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	tsText := string(tsBytes)
	if !strings.Contains(tsText, `counts: components["schemas"]["NullableTeacherMarkCounts"]`) || !strings.Contains(tsText, "NullableTeacherMarkCounts: {\n") {
		t.Fatalf("TypeScript SDK missing nullable TeacherRefreshRun.counts")
	}
	if !strings.Contains(tsText, `latest: components["schemas"]["NullableTeacherRefreshRun"]`) || !strings.Contains(tsText, "NullableTeacherRefreshRun: {\n") {
		t.Fatalf("TypeScript SDK missing nullable TeacherRefreshStatus.latest")
	}
	if !strings.Contains(tsText, "} | null;") {
		t.Fatalf("TypeScript SDK missing | null union on nullable component schemas")
	}

	specBytes, err := os.ReadFile("contracts/catalog.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"Object", "NullableObject"}, {"TeacherMarkCounts", "NullableTeacherMarkCounts"}, {"TeacherRefreshRun", "NullableTeacherRefreshRun"}} {
		base := spec.Components.Schemas[pair[0]]
		nullable := spec.Components.Schemas[pair[1]]
		if base == nil || nullable == nil || nullable["nullable"] != true {
			t.Fatalf("missing nullable schema pair %v", pair)
		}
		if !reflect.DeepEqual(base["properties"], nullable["properties"]) || !reflect.DeepEqual(base["required"], nullable["required"]) {
			t.Fatalf("schema drift between %s and %s", pair[0], pair[1])
		}
	}

	// A-11(2): Preview endpoints work without Idempotency-Key and reject malformed Idempotency-Key;
	// SDK params make IdempotencyKey optional (*string).
	if reflect.TypeOf(sdk.PreviewMergeParams{}).Field(0).Type.Kind() != reflect.Pointer ||
		reflect.TypeOf(sdk.PreviewImportParams{}).Field(0).Type.Kind() != reflect.Pointer {
		t.Fatalf("expected optional *string IdempotencyKey in preview SDK params")
	}
	badPreview := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/objects/course/merge-preview", strings.NewReader(`{"target_id":"course-dept","source_revision":1,"target_revision":1,"reason":"dup"}`))
	badPreview.Header.Set("X-Role", "admin")
	badPreview.Header.Set("Idempotency-Key", "bad\x7fkey")
	badPrevRec := httptest.NewRecorder()
	routes.ServeHTTP(badPrevRec, badPreview)
	if badPrevRec.Code != http.StatusBadRequest {
		t.Fatalf("malformed Idempotency-Key on merge-preview returned %d", badPrevRec.Code)
	}
}
