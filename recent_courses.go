package catalog

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const RecentCourseLimit = 20
const RecentCourseRetention = 90 * 24 * time.Hour

type RecentCourse struct {
	Course        Object    `json:"course"`
	VisitedAt     time.Time `json:"visited_at"`
	ResourceCount *int64    `json:"resource_count"`
}
type RecentCourses struct {
	Items          []RecentCourse `json:"items"`
	ResourcesState string         `json:"resources_state"`
}
type RecentResourceCounts interface {
	CountRecentResources(context.Context, Principal, string, []string) (map[string]int64, error)
}
type RecentCourseTransaction interface {
	SaveRecentCourse(string, Object, time.Time) error
	RecentCourses(string, string, time.Time) ([]RecentCourse, error)
	ClearRecentCourses(string) error
}
type RecentCourseRepository interface {
	PruneRecentCourses(context.Context, time.Time, int) (int64, error)
}

func (s *Service) RecordRecentCourse(ctx context.Context, p Principal, id string) error {
	object, err := s.readObject(ctx, p, id)
	if err != nil {
		return err
	}
	return s.saveRecentCourse(ctx, p, object)
}

func (s *Service) saveRecentCourse(ctx context.Context, p Principal, object Object) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if object.Kind != "course" || object.Inactive {
		return ErrInput
	}
	return s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(RecentCourseTransaction)
		if !ok {
			return ErrUnavailable
		}
		return port.SaveRecentCourse(p.Subject, object, s.now())
	})
}

func (s *Service) recordRecentCourse(ctx context.Context, p Principal, object Object) {
	if object.Kind != "course" {
		return
	}
	call, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := s.saveRecentCourse(call, p, object); err != nil {
		logger := s.OwnLogger
		if logger == nil {
			logger = slog.Default()
		}
		reason := "unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		} else if errors.Is(err, context.Canceled) {
			reason = "canceled"
		}
		logger.WarnContext(ctx, "catalog recent course write deferred", "reason", reason)
	}
}

func (s *Service) MyRecentCourses(ctx context.Context, p Principal) (RecentCourses, error) {
	out := RecentCourses{Items: []RecentCourse{}, ResourcesState: "unavailable"}
	school, err := s.readScope(ctx, p, "")
	if err != nil {
		return out, err
	}
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(RecentCourseTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out.Items, err = port.RecentCourses(p.Subject, school, s.now())
		return err
	})
	if err != nil || s.RecentResources == nil {
		return out, err
	}
	if len(out.Items) == 0 {
		out.ResourcesState = "available"
		return out, nil
	}
	ids := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		ids = append(ids, item.Course.ID)
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	counts, err := s.RecentResources.CountRecentResources(callCtx, p, school, ids)
	if err != nil {
		if ctx.Err() != nil {
			return RecentCourses{}, ctx.Err()
		}
		return out, nil
	}
	for _, id := range ids {
		if count, ok := counts[id]; !ok || count < 0 {
			return out, nil
		}
	}
	for i := range out.Items {
		count := counts[out.Items[i].Course.ID]
		out.Items[i].ResourceCount = &count
	}
	out.ResourcesState = "available"
	return out, nil
}

func (s *Service) ClearRecentCourses(ctx context.Context, p Principal) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	return s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(RecentCourseTransaction)
		if !ok {
			return ErrUnavailable
		}
		return port.ClearRecentCourses(p.Subject)
	})
}

func (s *Service) PruneRecentCourses(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInput
	}
	repo, ok := s.Repo.(RecentCourseRepository)
	if !ok {
		return 0, ErrUnavailable
	}
	return repo.PruneRecentCourses(ctx, s.now(), limit)
}
