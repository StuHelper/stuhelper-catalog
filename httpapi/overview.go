package httpapi

import (
	c "github.com/StuHelper/stuhelper-catalog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func readDirectoryIDList(q url.Values, key string) ([]string, error) {
	values, ok := q[key]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 || values[0] == "" {
		return nil, c.ErrInput
	}
	parts := strings.Split(values[0], ",")
	if len(parts) > 20 {
		return nil, c.ErrInput
	}
	for _, part := range parts {
		if !c.ValidID(part) {
			return nil, c.ErrInput
		}
	}
	return parts, nil
}

func readDirectoryCredits(q url.Values, key string) (*float64, error) {
	values, ok := q[key]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 || values[0] == "" {
		return nil, c.ErrInput
	}
	value, err := strconv.ParseFloat(values[0], 64)
	if err != nil {
		return nil, c.ErrInput
	}
	return &value, nil
}

func (h Handler) overviewRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/catalog/directory", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		filter, err := readFilter(req)
		if err != nil {
			respond(w, nil, err)
			return
		}
		q := req.URL.Query()
		deptIDs, err := readDirectoryIDList(q, "department_ids")
		if err != nil {
			respond(w, nil, err)
			return
		}
		catIDs, err := readDirectoryIDList(q, "category_ids")
		if err != nil {
			respond(w, nil, err)
			return
		}
		creditsMin, err := readDirectoryCredits(q, "credits_min")
		if err != nil {
			respond(w, nil, err)
			return
		}
		creditsMax, err := readDirectoryCredits(q, "credits_max")
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := h.Service.ReadDirectory(req.Context(), p, c.DirectoryQuery{
			Filter:        filter,
			DepartmentIDs: deptIDs,
			CategoryIDs:   catIDs,
			CreditsMin:    creditsMin,
			CreditsMax:    creditsMax,
			Sort:          q.Get("sort"),
		})
		respond(w, v, err)
	})
	m.HandleFunc("GET /api/v1/catalog/me/recent-courses", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		if len(req.URL.Query()) != 0 {
			respond(w, nil, c.ErrInput)
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
		if len(req.URL.Query()) != 0 {
			respond(w, nil, c.ErrInput)
			return
		}
		if req.ContentLength != 0 {
			var body struct{}
			if err := decode(w, req, &body); err != nil {
				respond(w, nil, err)
				return
			}
		}
		err := h.Service.RecordRecentCourse(req.Context(), p, req.PathValue("id"))
		respond(w, map[string]bool{"recorded": err == nil}, err)
	})
	m.HandleFunc("DELETE /api/v1/catalog/me/recent-courses", func(w http.ResponseWriter, req *http.Request) {
		p, ok := h.principal(w, req)
		if !ok {
			return
		}
		if len(req.URL.Query()) != 0 {
			respond(w, nil, c.ErrInput)
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
		if len(req.URL.Query()) != 0 {
			respond(w, nil, c.ErrInput)
			return
		}
		v, err := h.Service.MyNextClassDay(req.Context(), p)
		respond(w, v, err)
	})
}
