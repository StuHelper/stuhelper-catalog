package httpapi

import "net/http"

func (h Handler) overviewRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/catalog/me/recent-courses", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		v, err := h.Service.MyRecentCourses(req.Context(), p)
		respond(w, v, err)
	})
	m.HandleFunc("PUT /api/v1/catalog/me/recent-courses/{id}", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		err := h.Service.RecordRecentCourse(req.Context(), p, req.PathValue("id"))
		respond(w, map[string]bool{"recorded": err == nil}, err)
	})
	m.HandleFunc("DELETE /api/v1/catalog/me/recent-courses", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		err := h.Service.ClearRecentCourses(req.Context(), p)
		respond(w, map[string]bool{"cleared": err == nil}, err)
	})
	m.HandleFunc("GET /api/v1/catalog/me/next-class-day", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		v, err := h.Service.MyNextClassDay(req.Context(), p)
		respond(w, v, err)
	})
}
