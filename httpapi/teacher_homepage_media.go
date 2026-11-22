package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/StuHelper/stuhelper-catalog"
)

func (h Handler) readTeacherHomepageMedia(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	out, err := h.Service.ReadTeacherHomepageMedia(r.Context(), p, r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writer := json.NewEncoder(w)
	// Keep source query strings within the JSON response budget when they
	// contain ampersands. This route serves JSON with nosniff.
	writer.SetEscapeHTML(false)
	_ = writer.Encode(out)
}

func (h Handler) teacherHomepageBodyImage(downloads chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		loadBytes := r.Method != http.MethodHead && h.ImageRedirect == nil
		if loadBytes {
			// A slow download holds at most one bounded image until the
			// server write deadline. HEAD never reads the stored bytes.
			select {
			case downloads <- struct{}{}:
				defer func() { <-downloads }()
			case <-r.Context().Done():
				respond(w, nil, catalog.ErrUnavailable)
				return
			}
		}
		image, err := h.Service.ReadTeacherHomepageBodyImage(r.Context(), p, r.PathValue("id"), r.PathValue("image_id"), loadBytes)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if h.ImageRedirect != nil {
			h.redirectTeacherImage(w, r, p, catalog.TeacherImageKey{Kind: catalog.BodyImageObject, Digest: image.ID, School: image.DeliverySchool, OwnerID: r.PathValue("id")})
			return
		}
		w.Header().Set("Content-Type", image.MediaType)
		w.Header().Set("Content-Length", strconv.FormatInt(image.SizeBytes, 10))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if loadBytes {
			_, _ = w.Write(image.Bytes)
		}
	}
}
