package httpapi

import (
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"net/http"
)

func (h Handler) filterOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	for key := range r.URL.Query() {
		if key != "kind" && key != "school_id" {
			respond(w, nil, c.ErrInput)
			return
		}
	}
	v, e := h.Service.ReadFilterOptions(r.Context(), p, r.URL.Query().Get("kind"), r.URL.Query().Get("school_id"))
	respond(w, v, e)
}
func (h Handler) teacherRefreshRoutes(m *http.ServeMux) {
	const root = "/api/v1/catalog/admin/schools/{school_id}/teacher-sites"
	m.HandleFunc("GET "+root+"/refresh", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.TeacherRefreshStatus(r.Context(), p, r.PathValue("school_id"))
		respond(w, v, e)
	})
	m.HandleFunc("POST "+root+"/refresh", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in struct {
			Mode string `json:"mode"`
		}
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.QueueTeacherRefresh(r.Context(), p, r.PathValue("school_id"), in.Mode, "manual", r.Header.Get("Idempotency-Key"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(v)
	})
	m.HandleFunc("GET "+root+"/unmatched", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		n, e := limit(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.ReadUnmatchedTeacherSites(r.Context(), p, r.PathValue("school_id"), r.URL.Query().Get("reason"), r.URL.Query().Get("after"), n)
		respond(w, v, e)
	})
}
