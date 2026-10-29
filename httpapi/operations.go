package httpapi

import (
	c "github.com/StuHelper/stuhelper-catalog"
	"net/http"
)

func (h Handler) operationRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/catalog/objects/{id}/resolution", func(w http.ResponseWriter, r *http.Request) {
		v, e := h.Service.Resolve(r.Context(), r.PathValue("id"))
		respond(w, v, e)
	})
	for _, operation := range []string{"merge-preview", "merge"} {
		m.HandleFunc("POST /api/v1/catalog/objects/{id}/"+operation, func(w http.ResponseWriter, r *http.Request) {
			p, ok := h.principal(w, r)
			if !ok {
				return
			}
			var in c.MergeInput
			if e := decode(w, r, &in); e != nil {
				respond(w, nil, e)
				return
			}
			if operation == "merge-preview" {
				v, e := h.Service.PreviewMerge(r.Context(), p, r.PathValue("id"), in)
				respond(w, v, e)
			} else {
				v, e := h.Service.Merge(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
				respond(w, v, e)
			}
		})
	}
	m.HandleFunc("POST /api/v1/catalog/objects/{id}/state", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in c.ObjectState
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.SetState(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/catalog/imports/preview", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in c.Import
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.PreviewImport(r.Context(), p, in)
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/catalog/imports/confirm", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in c.Import
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		if in.PreviewToken == "" {
			respond(w, nil, c.ErrInput)
			return
		}
		v, e := h.Service.Import(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
		respond(w, v, e)
	})
}
