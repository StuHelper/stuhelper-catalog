// Package catalog owns the public academic directory and private enrollment evidence.
package catalog

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrInput           = errors.New("invalid_input")
	ErrNotFound        = errors.New("not_found")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrUnavailable     = errors.New("unavailable")
)

// Subject is an issuer-qualified account key supplied by verified authentication.
type Principal struct {
	Subject     string
	Schools     []string
	CanImport   bool
	CanMaintain bool
}
type Object struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	SchoolID     string    `json:"school_id"`
	Name         string    `json:"name"`
	Code         string    `json:"code,omitempty"`
	DepartmentID string    `json:"department_id,omitempty"`
	CategoryID   string    `json:"category_id,omitempty"`
	NatureID     string    `json:"nature_id,omitempty"`
	CourseID     string    `json:"course_id,omitempty"`
	TermID       string    `json:"term_id,omitempty"`
	TermCourseID string    `json:"term_course_id,omitempty"`
	ClassID      string    `json:"class_id,omitempty"`
	TeacherID    string    `json:"teacher_id,omitempty"`
	Credits      *float64  `json:"credits"`
	Biography    string    `json:"biography,omitempty"`
	Location     string    `json:"location,omitempty"`
	TimeText     string    `json:"time_text,omitempty"`
	Aliases      []string  `json:"aliases"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Record retains provenance separately from the public directory projection.
type Record struct {
	Object         Object            `json:"object"`
	SourceID       string            `json:"source_id"`
	SourceRevision int64             `json:"source_revision"`
	Imported       Object            `json:"imported"`
	Manual         map[string]string `json:"manual"`
}
type Enrollment struct {
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	SchoolID       string    `json:"school_id"`
	CourseID       string    `json:"course_id"`
	TermID         string    `json:"term_id"`
	ClassID        string    `json:"class_id,omitempty"`
	TeacherIDs     []string  `json:"teacher_ids"`
	SourceID       string    `json:"source_id"`
	SourceRevision int64     `json:"source_revision"`
	FetchedAt      time.Time `json:"fetched_at"`
	Active         bool      `json:"active"`
}
type Import struct {
	SchoolID    string       `json:"school_id"`
	SourceID    string       `json:"source_id"`
	Revision    int64        `json:"revision"`
	FetchedAt   time.Time    `json:"fetched_at"`
	Objects     []Object     `json:"objects"`
	Enrollments []Enrollment `json:"enrollments"`
}
type ImportResult struct {
	ID                   string `json:"id"`
	Objects              int    `json:"objects"`
	Enrollments          int    `json:"enrollments"`
	PreservedCorrections int    `json:"preserved_corrections"`
}
type Filter struct {
	Kind, SchoolID, DepartmentID, CategoryID, NatureID, CourseID, TermID, TeacherID, Query, After string
	Limit                                                                                         int
}
type Page struct {
	Items []Object `json:"items"`
	Next  string   `json:"next"`
}
type Correction struct {
	Revision  int64   `json:"revision"`
	Name      *string `json:"name,omitempty"`
	Biography *string `json:"biography,omitempty"`
}
type Event struct {
	ID, Subject, Type, SchoolID, Actor string
	Revision                           int64
	Time                               time.Time
}
type Replay struct {
	Digest string
	Result []byte
	Until  time.Time
}
type Repository interface {
	Transaction(context.Context, func(Transaction) error) error
}
type Transaction interface {
	Lock(string) error
	Object(string) (Record, error)
	SaveObject(Record) error
	List(Filter) ([]Object, error)
	HasAssignment(string, string) (bool, error)
	Enrollment(string) (Enrollment, error)
	SaveEnrollment(Enrollment) error
	Enrollments(subject, school, course, term, after string, limit int) ([]Enrollment, error)
	Replay(string) (Replay, error)
	SaveReplay(string, Replay) error
	Event(Event) error
}
