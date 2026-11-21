package httpapi

import (
	"net/http"
	"strconv"

	"github.com/StuHelper/stuhelper-catalog"
)

func writeTeacherHomepagePhoto(w http.ResponseWriter, r *http.Request, photo catalog.TeacherHomepageImage) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if (photo.MediaType != "image/jpeg" && photo.MediaType != "image/png") ||
		len(photo.Bytes) == 0 || len(photo.Bytes) > catalog.TeacherHomepageMaxPhotoBytes {
		respond(w, nil, catalog.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", photo.MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(photo.Bytes)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(photo.Bytes)
	}
}

func (h Handler) teacherHomepageRequestPhoto(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	photo, err := h.Service.ReadTeacherHomepageRequestPhoto(r.Context(), p, r.PathValue("school_id"), r.PathValue("request_id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeTeacherHomepagePhoto(w, r, photo)
}
