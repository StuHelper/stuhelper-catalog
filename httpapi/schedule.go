package httpapi

import (
	c "github.com/StuHelper/stuhelper-catalog"
	"net/http"
)

func (h Handler) scheduleRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/catalog/me/schedule", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		if !c.ValidID(q.Get("term_id")) || q.Has("subject") || q.Has("user_id") {
			respond(w, nil, c.ErrInput)
			return
		}
		n, e := limit(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		v, e := h.Service.MySchedule(r.Context(), p, q.Get("term_id"), q.Get("after"), n)
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/me/schedule/calendar", func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		if r.URL.Query().Has("subject") || r.URL.Query().Has("user_id") {
			respond(w, nil, c.ErrInput)
			return
		}
		v, e := h.Service.MyCalendar(r.Context(), p, r.URL.Query().Get("term_id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="schedule.ics"`)
		_, _ = w.Write([]byte(v))
	})
	path := "/api/v1/catalog/admin/schools/{school_id}/terms/{term_id}/timetable"
	m.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		v, e := h.Service.GetTimetable(r.Context(), p, r.PathValue("school_id"), r.PathValue("term_id"))
		respond(w, v, e)
	})
	m.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
		p, ok := h.principal(w, r)
		if !ok {
			return
		}
		var in c.Timetable
		if e := decode(w, r, &in); e != nil {
			respond(w, nil, e)
			return
		}
		if in.SchoolID != r.PathValue("school_id") || in.TermID != r.PathValue("term_id") {
			respond(w, nil, c.ErrInput)
			return
		}
		v, e := h.Service.PutTimetable(r.Context(), p, in)
		respond(w, v, e)
	})
}
