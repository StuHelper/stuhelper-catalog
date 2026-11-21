package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/StuHelper/stuhelper-catalog"
)

func (h Handler) teacherHomepageControlRoutes(m *http.ServeMux) {
	const admin = "/api/v1/catalog/admin/schools/{school_id}/teacher-sites"
	m.HandleFunc("GET /api/v1/catalog/objects/{id}/teacher-homepage", h.readTeacherHomepage)
	m.HandleFunc("GET "+admin+"/teachers/{teacher_id}/homepage", h.readTeacherHomepageControls)
	m.HandleFunc("PUT "+admin+"/teachers/{teacher_id}/homepage", h.setTeacherHomepageControls)
	m.HandleFunc("GET "+admin+"/allowlist", h.readTeacherHomepageAllowlist)
	m.HandleFunc("PUT "+admin+"/allowlist", h.setTeacherHomepageAllowlist)
	m.HandleFunc("GET "+admin+"/homepage-counts", h.readTeacherHomepageCounts)
}

type teacherHomepageSelectorInput struct {
	Field string          `json:"field"`
	Label json.RawMessage `json:"label"`
}

func (in teacherHomepageSelectorInput) decode() (catalog.TeacherHomepageFieldSelector, error) {
	field := catalog.TeacherHomepageFieldSelector{Field: in.Field}
	if in.Label != nil && (json.Unmarshal(in.Label, &field.Label) != nil || field.Label == "") {
		return field, catalog.ErrInput
	}
	return field, nil
}

type teacherHomepageRuleInput struct {
	Field    teacherHomepageSelectorInput `json:"field"`
	Hidden   *bool                        `json:"hidden"`
	Override json.RawMessage              `json:"override"`
}

func (in teacherHomepageRuleInput) decode() (catalog.TeacherHomepageFieldRule, error) {
	field, err := in.Field.decode()
	if err != nil || in.Hidden == nil {
		return catalog.TeacherHomepageFieldRule{}, catalog.ErrInput
	}
	rule := catalog.TeacherHomepageFieldRule{Field: field, Hidden: *in.Hidden}
	if in.Override != nil {
		d := json.NewDecoder(bytes.NewReader(in.Override))
		d.DisallowUnknownFields()
		if d.Decode(&rule.Override) != nil || rule.Override == nil {
			return rule, catalog.ErrInput
		}
	}
	return rule, nil
}

func (h Handler) readTeacherHomepage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	view, err := h.Service.ReadTeacherHomepage(r.Context(), p, r.PathValue("id"))
	respond(w, view, err)
}

func (h Handler) readTeacherHomepageControls(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	view, err := h.Service.ReadTeacherHomepageControls(r.Context(), p, r.PathValue("school_id"), r.PathValue("teacher_id"))
	respond(w, view, err)
}

func (h Handler) setTeacherHomepageControls(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedRevision *int64                     `json:"expected_revision"`
		PageVisibility   string                     `json:"page_visibility"`
		Rules            []teacherHomepageRuleInput `json:"rules"`
	}
	if !decodeTeacherHomepageInput(w, r, &body) {
		return
	}
	if body.Rules == nil || len(body.Rules) > 256 {
		respond(w, nil, catalog.ErrInput)
		return
	}
	in := catalog.TeacherHomepageControlsInput{
		ExpectedRevision: body.ExpectedRevision, PageVisibility: body.PageVisibility,
		Rules: make([]catalog.TeacherHomepageFieldRule, 0, len(body.Rules)),
	}
	for _, raw := range body.Rules {
		rule, err := raw.decode()
		if err != nil {
			respond(w, nil, err)
			return
		}
		in.Rules = append(in.Rules, rule)
	}
	result, err := h.Service.SetTeacherHomepageControls(r.Context(), p, r.PathValue("school_id"), r.PathValue("teacher_id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, result, err)
}

func (h Handler) readTeacherHomepageAllowlist(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	result, err := h.Service.ReadTeacherHomepageAllowlist(r.Context(), p, r.PathValue("school_id"))
	respond(w, result, err)
}

func (h Handler) setTeacherHomepageAllowlist(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedRevision *int64                         `json:"expected_revision"`
		Fields           []teacherHomepageSelectorInput `json:"fields"`
	}
	if err := decode(w, r, &body); err != nil || body.Fields == nil || len(body.Fields) > 256 {
		respond(w, nil, catalog.ErrInput)
		return
	}
	in := catalog.TeacherHomepageAllowlistInput{
		ExpectedRevision: body.ExpectedRevision,
		Fields:           make([]catalog.TeacherHomepageFieldSelector, 0, len(body.Fields)),
	}
	for _, raw := range body.Fields {
		field, err := raw.decode()
		if err != nil {
			respond(w, nil, err)
			return
		}
		in.Fields = append(in.Fields, field)
	}
	result, err := h.Service.SetTeacherHomepageAllowlist(r.Context(), p, r.PathValue("school_id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, result, err)
}

func (h Handler) readTeacherHomepageCounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	result, err := h.Service.ReadTeacherHomepageCounts(r.Context(), p, r.PathValue("school_id"))
	respond(w, result, err)
}
