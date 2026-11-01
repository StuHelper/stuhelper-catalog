package catalog

import (
	"context"
	"time"
)

// ContentAccess resolves the served school from current server-owned facts.
// Internal catalog facts remain available to authorized module composition.
type ContentAccess interface {
	Scope(context.Context, Principal, string) (string, error)
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
func (s *Service) Read(ctx context.Context, p Principal, id string) (Object, error) {
	school, e := s.readScope(ctx, p, "")
	if e != nil {
		return Object{}, e
	}
	object, e := s.Get(ctx, id)
	if e != nil {
		return Object{}, e
	}
	if object.SchoolID != school {
		return Object{}, ErrForbidden
	}
	return object, nil
}
func (s *Service) ReadList(ctx context.Context, p Principal, f Filter) (Page, error) {
	school, e := s.readScope(ctx, p, f.SchoolID)
	if e != nil {
		return Page{}, e
	}
	f.SchoolID = school
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
	for _, item := range out.Items {
		if item.SchoolID != school {
			return Lookup{}, ErrForbidden
		}
	}
	return out, nil
}
func (s *Service) ReadSummary(ctx context.Context, p Principal, id string) (Summary, error) {
	if _, e := s.Read(ctx, p, id); e != nil {
		return Summary{}, e
	}
	return s.Summary(ctx, id)
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
	if _, e := s.AdminRead(ctx, p, id); e != nil {
		return Summary{}, e
	}
	return s.Summary(ctx, id)
}
func (s *Service) AdminResolution(ctx context.Context, p Principal, id string) (Resolution, error) {
	if _, e := s.AdminRead(ctx, p, id); e != nil {
		return Resolution{}, e
	}
	return s.Resolve(ctx, id)
}
