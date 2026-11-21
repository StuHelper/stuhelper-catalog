package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/StuHelper/stuhelper-catalog"
)

func teacherHomepageRequested(r *http.Request) (bool, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false, catalog.ErrInput
	}
	values := query["include_teacher_homepage"]
	if values == nil {
		return false, nil
	}
	if len(values) != 1 || values[0] != "true" && values[0] != "false" {
		return false, catalog.ErrInput
	}
	return values[0] == "true", nil
}

func (h Handler) teacherHomepageRoutes(m *http.ServeMux) {
	const root = "/api/v1/catalog/admin/schools/{school_id}/teacher-sites/review"
	h.teacherHomepageControlRoutes(m)
	m.HandleFunc("GET /api/v1/catalog/objects/{id}/teacher-homepage/photos/{photo_id}", h.teacherHomepagePhoto)
	m.HandleFunc("GET "+root+"/sites", h.teacherHomepageReviewSites)
	m.HandleFunc("GET "+root+"/teachers", h.teacherHomepageReviewTeachers)
	m.HandleFunc("PUT "+root+"/sites/{site_id}/decision", h.teacherHomepageDecision)
}

func (h Handler) teacherHomepagePhoto(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	photo, err := h.Service.ReadTeacherHomepagePhoto(r.Context(), p, r.PathValue("id"), r.PathValue("photo_id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	writeTeacherHomepagePhoto(w, r, photo)
}

func teacherHomepageReviewQuery(r *http.Request) (catalog.TeacherReviewQuery, error) {
	var out catalog.TeacherReviewQuery
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return out, catalog.ErrInput
	}
	for name, values := range query {
		switch name {
		case "status", "q", "after", "limit":
			if len(values) != 1 || values[0] == "" {
				return out, catalog.ErrInput
			}
		default:
			return out, catalog.ErrInput
		}
	}
	n, err := limit(r)
	if err != nil {
		return out, err
	}
	return catalog.TeacherReviewQuery{
		SchoolID: r.PathValue("school_id"), Status: query.Get("status"), Query: query.Get("q"),
		After: query.Get("after"), Limit: n,
	}, nil
}

func (h Handler) teacherHomepageReviewSites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	query, err := teacherHomepageReviewQuery(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := h.Service.ReadTeacherSiteReviews(r.Context(), p, query)
	respond(w, out, err)
}

func (h Handler) teacherHomepageReviewTeachers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	query, err := teacherHomepageReviewQuery(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := h.Service.ReadTeacherReviews(r.Context(), p, query)
	respond(w, out, err)
}

func (h Handler) teacherHomepageDecision(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Action           string          `json:"action"`
		TeacherID        json.RawMessage `json:"teacher_id"`
		ExpectedRevision *int64          `json:"expected_revision"`
	}
	if err := decode(w, r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	in := catalog.TeacherSiteDecisionInput{Action: body.Action, ExpectedRevision: body.ExpectedRevision}
	if len(body.TeacherID) > 0 {
		if body.Action != "bind" || json.Unmarshal(body.TeacherID, &in.TeacherID) != nil {
			respond(w, nil, catalog.ErrInput)
			return
		}
	}
	out, err := h.Service.SetTeacherSiteDecision(r.Context(), p, r.PathValue("school_id"), r.PathValue("site_id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, out, err)
}
