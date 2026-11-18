package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (h Handler) adminReadRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/catalog/admin/schools/{school_id}/directory-projection", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		if len(r.URL.Query()) != 0 {
			respond(w, nil, c.ErrInput)
			return
		}
		v, e := h.Service.AdminDirectoryProjection(r.Context(), p, r.PathValue("school_id"))
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/catalog/admin/schools/{school_id}/directory-projection", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in struct {
			Policy *c.DirectoryPolicy `json:"policy"`
		}
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.QueueDirectoryRebuild(r.Context(), p, r.PathValue("school_id"), r.Header.Get("Idempotency-Key"), in.Policy)
		if e != nil {
			respond(w, nil, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(v)
	})
	m.HandleFunc("GET /api/v1/catalog/admin/objects", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		f, e := readFilter(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.AdminList(r.Context(), p, f)
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/admin/objects/lookup", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.AdminLookup(r.Context(), p, strings.Split(r.URL.Query().Get("ids"), ","))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/admin/objects/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.AdminRead(r.Context(), p, r.PathValue("id"))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/admin/objects/{id}/summary", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.AdminSummary(r.Context(), p, r.PathValue("id"))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/admin/objects/{id}/resolution", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.AdminResolution(r.Context(), p, r.PathValue("id"))
		respond(w, v, e)
	})
}
