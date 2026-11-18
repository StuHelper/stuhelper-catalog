package catalog

import (
	"context"
	"slices"
	"time"
)

// ContentAccess resolves the served school from current server-owned facts.
// Internal catalog facts remain available to authorized module composition.
type ContentAccess interface {
	Scope(context.Context, Principal, string) (string, error)
}

// Teaching details remain available through scoped maintenance and owner-only
// APIs. Trusted module ports also need them to validate enrollment and reviews.
func teachingDetail(kind string) bool {
	return kind == "term_course" || kind == "class" || kind == "assignment" || kind == "schedule"
}

func publicFilter(f Filter) error {
	if teachingDetail(f.Kind) || f.TermCourseID != "" || f.ClassID != "" {
		return ErrForbidden
	}
	return nil
}

func (s *Service) readScope(ctx context.Context, p Principal, requested string) (string, error) {
	if p.Subject == "" {
		return "", ErrUnauthenticated
	}
	if s.ReadAccess == nil {
		return "", ErrUnavailable
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	school, e := s.ReadAccess.Scope(callCtx, p, requested)
	if e != nil {
		return "", e
	}
	if !ValidID(school) || requested != "" && requested != school {
		return "", ErrUnavailable
	}
	return school, nil
}
func (s *Service) readObject(ctx context.Context, p Principal, id string) (Object, error) {
	school, e := s.readScope(ctx, p, "")
	if e != nil {
		return Object{}, e
	}
	object, e := s.Get(ctx, id)
	if e != nil {
		return Object{}, e
	}
	if teachingDetail(object.Kind) || object.Inactive {
		return Object{}, ErrNotFound
	}
	if object.SchoolID != school {
		return Object{}, ErrForbidden
	}
	return object, nil
}
func (s *Service) ReadList(ctx context.Context, p Principal, f Filter) (Page, error) {
	start := time.Now()
	school, e := s.readScope(ctx, p, f.SchoolID)
	s.observeDirectory(ctx, "authorization", start, e)
	if e != nil {
		return Page{}, e
	}
	if e := publicFilter(f); e != nil {
		return Page{}, e
	}
	f.SchoolID = school
	f.IncludeInactive = false
	return s.List(ctx, f)
}
func (s *Service) ReadLookup(ctx context.Context, p Principal, ids []string) (Lookup, error) {
	school, e := s.readScope(ctx, p, "")
	if e != nil {
		return Lookup{}, e
	}
	out, e := s.Lookup(ctx, ids)
	if e != nil {
		return Lookup{}, e
	}
	visible := make([]Object, 0, len(out.Items))
	for _, item := range out.Items {
		if teachingDetail(item.Kind) || item.Inactive {
			out.MissingIDs = append(out.MissingIDs, item.ID)
			continue
		}
		if item.SchoolID != school {
			return Lookup{}, ErrForbidden
		}
		visible = append(visible, item)
	}
	out.Items = visible
	slices.Sort(out.MissingIDs)
	return out, nil
}
func (s *Service) Read(ctx context.Context, p Principal, id string) (Object, error) {
	object, e := s.readObject(ctx, p, id)
	if e != nil {
		return object, e
	}
	s.recordView(ctx, p, object)
	s.recordRecentCourse(ctx, p, object)
	return object, nil
}
func (s *Service) ReadSummary(ctx context.Context, p Principal, id string) (PublicSummary, error) {
	object, e := s.readObject(ctx, p, id)
	if e != nil {
		return PublicSummary{}, e
	}
	summary, e := s.Summary(ctx, id)
	if e != nil {
		return PublicSummary{}, e
	}
	dept := summary.Department
	if dept != nil && dept.Inactive {
		dept = nil
	}
	s.recordView(ctx, p, object)
	s.recordRecentCourse(ctx, p, object)
	courses := summary.Courses
	if courses == nil {
		courses = []Reference{}
	}
	teachers := summary.Teachers
	if teachers == nil {
		teachers = []Reference{}
	}
	return PublicSummary{
		Object: summary.Object, School: summary.School, Department: dept,
		Categories: summary.Categories, Natures: summary.Natures,
		Courses: courses, Teachers: teachers,
		CourseCount: summary.CourseCount, TeacherCount: summary.TeacherCount,
	}, nil
}
func (s *Service) ReadResolution(ctx context.Context, p Principal, id string) (Resolution, error) {
	school, e := s.readScope(ctx, p, "")
	if e != nil {
		return Resolution{}, e
	}
	out, e := s.Resolve(ctx, id)
	if e != nil {
		return Resolution{}, e
	}
	if teachingDetail(out.Object.Kind) || out.Object.Inactive {
		return Resolution{}, ErrNotFound
	}
	if out.Object.SchoolID != school {
		return Resolution{}, ErrForbidden
	}
	return out, nil
}

func maintainRead(p Principal) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if !p.CanMaintain || len(p.Schools) == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *Service) AdminRead(ctx context.Context, p Principal, id string) (Object, error) {
	ctx = teacherMaintenanceContext(ctx)
	if e := maintainRead(p); e != nil {
		return Object{}, e
	}
	out, e := s.Get(ctx, id)
	if e != nil {
		return Object{}, e
	}
	if !allowed(p, out.SchoolID) {
		return Object{}, ErrForbidden
	}
	return out, nil
}
func (s *Service) AdminList(ctx context.Context, p Principal, f Filter) (Page, error) {
	ctx = teacherMaintenanceContext(ctx)
	if e := maintainRead(p); e != nil {
		return Page{}, e
	}
	if !ValidID(f.SchoolID) {
		return Page{}, ErrInput
	}
	if !allowed(p, f.SchoolID) {
		return Page{}, ErrForbidden
	}
	return s.List(ctx, f)
}
func (s *Service) AdminLookup(ctx context.Context, p Principal, ids []string) (Lookup, error) {
	ctx = teacherMaintenanceContext(ctx)
	if e := maintainRead(p); e != nil {
		return Lookup{}, e
	}
	out, e := s.Lookup(ctx, ids)
	if e != nil {
		return Lookup{}, e
	}
	for _, item := range out.Items {
		if !allowed(p, item.SchoolID) {
			return Lookup{}, ErrForbidden
		}
	}
	return out, nil
}
func (s *Service) AdminSummary(ctx context.Context, p Principal, id string) (Summary, error) {
	ctx = teacherMaintenanceContext(ctx)
	if _, e := s.AdminRead(ctx, p, id); e != nil {
		return Summary{}, e
	}
	return s.Summary(ctx, id)
}
func (s *Service) AdminResolution(ctx context.Context, p Principal, id string) (Resolution, error) {
	ctx = teacherMaintenanceContext(ctx)
	if _, e := s.AdminRead(ctx, p, id); e != nil {
		return Resolution{}, e
	}
	return s.Resolve(ctx, id)
}
