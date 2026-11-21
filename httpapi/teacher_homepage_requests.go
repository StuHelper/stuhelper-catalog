package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/StuHelper/stuhelper-catalog"
)

func (h Handler) teacherHomepageRequestRoutes(m *http.ServeMux) {
	const own = "/api/v1/catalog/objects/{id}/teacher-homepage/requests"
	const admin = "/api/v1/catalog/admin/schools/{school_id}/teacher-sites/requests"
	m.HandleFunc("GET "+own, h.listMyTeacherHomepageRequests)
	m.HandleFunc("POST "+own, h.submitTeacherHomepageRequest)
	m.HandleFunc("GET /api/v1/catalog/me/teacher-homepage/requests/{request_id}", h.getMyTeacherHomepageRequest)
	m.HandleFunc("GET "+admin, h.listTeacherHomepageRequests)
	m.HandleFunc("GET "+admin+"/{request_id}", h.getTeacherHomepageRequest)
	m.HandleFunc("PUT "+admin+"/{request_id}/decision", h.resolveTeacherHomepageRequest)
	m.HandleFunc("GET "+admin+"/{request_id}/photo", h.teacherHomepageRequestPhoto)
}

func parseTeacherHomepageRequestQuery(r *http.Request, admin bool) (catalog.TeacherHomepageRequestQuery, error) {
	var out catalog.TeacherHomepageRequestQuery
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return out, catalog.ErrInput
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return out, catalog.ErrInput
		}
		switch key {
		case "limit", "after", "status":
		case "teacher_id":
			if !admin {
				return out, catalog.ErrInput
			}
		default:
			return out, catalog.ErrInput
		}
	}
	size, err := limit(r)
	if err != nil {
		return out, err
	}
	return catalog.TeacherHomepageRequestQuery{
		Status: query.Get("status"), After: query.Get("after"), TeacherID: query.Get("teacher_id"), Limit: size,
	}, nil
}

func (h Handler) listMyTeacherHomepageRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	in, err := parseTeacherHomepageRequestQuery(r, false)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := h.Service.ListMyTeacherHomepageRequests(r.Context(), p, r.PathValue("id"), in)
	respond(w, out, err)
}

func (h Handler) submitTeacherHomepageRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind    string                               `json:"kind"`
		Field   catalog.TeacherHomepageFieldSelector `json:"field"`
		Value   json.RawMessage                      `json:"value"`
		Message string                               `json:"message"`
	}
	if !decodeTeacherHomepageInput(w, r, &body) {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		respond(w, nil, catalog.ErrInput)
		return
	}
	in := catalog.TeacherHomepageRequestInput{Kind: body.Kind, Field: body.Field, Message: body.Message}
	if len(body.Value) != 0 {
		data := json.NewDecoder(bytes.NewReader(body.Value))
		data.DisallowUnknownFields()
		if data.Decode(&in.Value) != nil || in.Value == nil {
			respond(w, nil, catalog.ErrInput)
			return
		}
	}
	out, err := h.Service.SubmitTeacherHomepageRequest(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, out, err)
}

func (h Handler) getMyTeacherHomepageRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	out, err := h.Service.GetMyTeacherHomepageRequest(r.Context(), p, r.PathValue("request_id"))
	respond(w, out, err)
}

func (h Handler) listTeacherHomepageRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	in, err := parseTeacherHomepageRequestQuery(r, true)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := h.Service.ListTeacherHomepageRequests(r.Context(), p, r.PathValue("school_id"), in)
	respond(w, out, err)
}

func (h Handler) getTeacherHomepageRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	out, err := h.Service.GetTeacherHomepageRequest(r.Context(), p, r.PathValue("school_id"), r.PathValue("request_id"))
	respond(w, out, err)
}

func (h Handler) resolveTeacherHomepageRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Action                   string          `json:"action"`
		ExpectedRevision         *int64          `json:"expected_revision"`
		ExpectedHomepageRevision json.RawMessage `json:"expected_homepage_revision"`
		Note                     string          `json:"note"`
	}
	if err := decode(w, r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		respond(w, nil, catalog.ErrInput)
		return
	}
	in := catalog.TeacherHomepageRequestDecisionInput{Action: body.Action, ExpectedRevision: body.ExpectedRevision, Note: body.Note}
	if len(body.ExpectedHomepageRevision) != 0 {
		if json.Unmarshal(body.ExpectedHomepageRevision, &in.ExpectedHomepageRevision) != nil || in.ExpectedHomepageRevision == nil {
			respond(w, nil, catalog.ErrInput)
			return
		}
	}
	out, err := h.Service.ResolveTeacherHomepageRequest(r.Context(), p, r.PathValue("school_id"), r.PathValue("request_id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, out, err)
}
