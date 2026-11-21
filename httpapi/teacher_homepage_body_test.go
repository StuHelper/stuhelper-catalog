package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTeacherHomepageInputSizeAndJSONErrors(t *testing.T) {
	const prefix, suffix = `{"text":"`, `"}`
	maxValue := strings.Repeat("x", teacherHomepageMaxBodyBytes-len(prefix)-len(suffix))
	for _, tc := range []struct {
		name    string
		body    string
		stream  bool
		allowed bool
		status  int
		code    string
	}{
		{name: "exact limit", body: prefix + maxValue + suffix, allowed: true, status: http.StatusOK},
		{name: "declared above limit", body: prefix + maxValue + suffix + " ", status: http.StatusRequestEntityTooLarge, code: "payload_too_large"},
		{name: "streamed above limit", body: prefix + maxValue + "x" + suffix, stream: true, status: http.StatusRequestEntityTooLarge, code: "payload_too_large"},
		{name: "streamed trailing above limit", body: prefix + maxValue + suffix + " ", stream: true, status: http.StatusRequestEntityTooLarge, code: "payload_too_large"},
		{name: "malformed JSON", body: prefix, status: http.StatusBadRequest, code: "invalid_input"},
		{name: "unknown field", body: `{"other":true}`, status: http.StatusBadRequest, code: "invalid_input"},
		{name: "extra object", body: `{}{}`, status: http.StatusBadRequest, code: "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.stream {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			var input struct {
				Text string `json:"text"`
			}
			if ok := decodeTeacherHomepageInput(w, r, &input); ok != tc.allowed || w.Code != tc.status {
				t.Fatalf("decode allowed = %v, status = %d, want %v and %d", ok, w.Code, tc.allowed, tc.status)
			}
			if !tc.allowed {
				var response map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response["code"] != tc.code {
					t.Fatalf("response = %v, err = %v, want %s", response, err, tc.code)
				}
			}
		})
	}
}
