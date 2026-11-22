package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/StuHelper/stuhelper-catalog"
)

const teacherHomepageMaxBodyBytes = 4 << 20

// The upload routes distinguish the transport limit from invalid fields or
// invalid decoded image data.
func decodeTeacherHomepageInput(w http.ResponseWriter, r *http.Request, input any) bool {
	if r.ContentLength > teacherHomepageMaxBodyBytes {
		return rejectTeacherHomepageInput(w, &http.MaxBytesError{Limit: teacherHomepageMaxBodyBytes})
	}
	r.Body = http.MaxBytesReader(w, r.Body, teacherHomepageMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		return rejectTeacherHomepageInput(w, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return rejectTeacherHomepageInput(w, err)
	}
	return true
}

func rejectTeacherHomepageInput(w http.ResponseWriter, err error) bool {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "payload_too_large"})
	} else {
		respond(w, nil, catalog.ErrInput)
	}
	return false
}
