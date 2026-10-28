// Package httpapi parses catalog requests; the service owns authorization and rules.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	catalog "github.com/StuHelper/stuhelper-catalog"
)

type Authenticate func(*http.Request) (catalog.Principal, error)
type Handler struct {
	Service      *catalog.Service
	Authenticate Authenticate
}

func (h Handler) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/catalog/objects", h.list)
	m.HandleFunc("GET /api/v1/catalog/objects/lookup", func(w http.ResponseWriter, r *http.Request) {
		v, e := h.Service.Lookup(r.Context(), strings.Split(r.URL.Query().Get("ids"), ","))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/objects/{id}/summary", func(w http.ResponseWriter, r *http.Request) {
		v, e := h.Service.Summary(r.Context(), r.PathValue("id"))
		respond(w, v, e)
	})
	m.HandleFunc("GET /api/v1/catalog/objects/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := h.Service.Get(r.Context(), r.PathValue("id"))
		respond(w, v, e)
	})
	m.HandleFunc("POST /api/v1/catalog/imports", h.importData)
	m.HandleFunc("PATCH /api/v1/catalog/objects/{id}", h.correct)
	m.HandleFunc("GET /api/v1/catalog/me/enrollments", h.enrollments)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" && r.Header.Get("Origin") != "" {
			u, e := url.Parse(r.Header.Get("Origin"))
			if e != nil || u.Host != r.Host {
				respond(w, nil, catalog.ErrForbidden)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		status := 503
		code := "unavailable"
		for _, item := range []struct {
			e error
			s int
		}{{catalog.ErrUnauthenticated, 401}, {catalog.ErrInput, 400}, {catalog.ErrForbidden, 403}, {catalog.ErrNotFound, 404}, {catalog.ErrConflict, 409}} {
			if errors.Is(e, item.e) {
				status = item.s
				code = item.e.Error()
			}
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return catalog.ErrInput
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return catalog.ErrInput
	}
	return nil
}
func (h Handler) principal(w http.ResponseWriter, r *http.Request) (catalog.Principal, bool) {
	if h.Authenticate == nil {
		respond(w, nil, catalog.ErrUnavailable)
		return catalog.Principal{}, false
	}
	p, e := h.Authenticate(r)
	if e != nil {
		respond(w, nil, e)
		return p, false
	}
	if p.Subject == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "unauthenticated"})
		return p, false
	}
	return p, true
}
func limit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 20, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 100 {
		return 0, catalog.ErrInput
	}
	return n, nil
}
func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	v, e := h.Service.List(r.Context(), catalog.Filter{Kind: q.Get("kind"), SchoolID: q.Get("school_id"), DepartmentID: q.Get("department_id"), CategoryID: q.Get("category_id"), NatureID: q.Get("nature_id"), CourseID: q.Get("course_id"), TermID: q.Get("term_id"), TeacherID: q.Get("teacher_id"), TermCourseID: q.Get("term_course_id"), ClassID: q.Get("class_id"), Query: q.Get("q"), After: q.Get("after"), Limit: n})
	respond(w, v, e)
}
func (h Handler) importData(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var in catalog.Import
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Import(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
func (h Handler) correct(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var in catalog.Correction
	if e := decode(w, r, &in); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := h.Service.Correct(r.Context(), p, r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
	respond(w, v, e)
}
func (h Handler) enrollments(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	n, e := limit(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	v, e := h.Service.MyEnrollments(r.Context(), p, q.Get("school_id"), q.Get("course_id"), q.Get("term_id"), q.Get("after"), n)
	respond(w, v, e)
}
