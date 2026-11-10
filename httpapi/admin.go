package httpapi

import (
	"net/http"
	"strings"
)

func (h Handler) adminReadRoutes(m *http.ServeMux) {
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
