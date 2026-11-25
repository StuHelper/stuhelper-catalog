package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/StuHelper/stuhelper-catalog"
)

// TeacherImageRedirect signs only after the service authorizes the current request.
type TeacherImageRedirect func(*http.Request, catalog.Principal, catalog.TeacherImageKey) (string, error)

func (h Handler) redirectTeacherImage(w http.ResponseWriter, r *http.Request, p catalog.Principal, key catalog.TeacherImageKey) {
	location, err := h.ImageRedirect(r, p, key)
	if err != nil {
		var limited catalog.TeacherImageRateLimit
		if errors.As(err, &limited) {
			w.Header().Set("Retry-After", strconv.Itoa(limited.RetryAfter))
		}
		respond(w, nil, err)
		return
	}
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

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
	if h.ImageRedirect != nil {
		key, err := h.Service.TeacherRequestPhotoForDelivery(r.Context(), p, r.PathValue("school_id"), r.PathValue("request_id"))
		if err != nil {
			respond(w, nil, err)
			return
		}
		h.redirectTeacherImage(w, r, p, key)
		return
	}
	photo, err := h.Service.ReadTeacherHomepageRequestPhoto(r.Context(), p, r.PathValue("school_id"), r.PathValue("request_id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeTeacherHomepagePhoto(w, r, photo)
}

func (h Handler) teacherDirectoryAvatar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	thumb, key, err := h.Service.ReadTeacherAvatar(r.Context(), p, r.PathValue("id"), r.PathValue("photo_id"), h.ImageRedirect == nil)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if h.ImageRedirect != nil {
		h.redirectTeacherImage(w, r, p, key)
		return
	}
	writeTeacherHomepagePhoto(w, r, thumb)
}
