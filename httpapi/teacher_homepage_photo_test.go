package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/StuHelper/stuhelper-catalog"
)

func TestWriteTeacherHomepagePhotoUsesPrivateSameOriginResponse(t *testing.T) {
	data := []byte("synthetic image bytes")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/synthetic-photo", nil)
			writeTeacherHomepagePhoto(w, r, catalog.TeacherHomepageImage{MediaType: "image/jpeg", Bytes: data})
			headers := w.Header()
			if w.Code != http.StatusOK || headers.Get("Cache-Control") != "private, no-store" ||
				headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Content-Type") != "image/jpeg" ||
				headers.Get("Content-Length") != strconv.Itoa(len(data)) {
				t.Fatalf("unexpected photo response %d %v", w.Code, headers)
			}
			length := len(data)
			if method == http.MethodHead {
				length = 0
			}
			if w.Body.Len() != length {
				t.Fatalf("response body length got %d want %d", w.Body.Len(), length)
			}
		})
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/synthetic-photo", nil)
	writeTeacherHomepagePhoto(w, r, catalog.TeacherHomepageImage{MediaType: "text/html", Bytes: data})
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unexpected invalid image response %d %v", w.Code, w.Header())
	}
}
