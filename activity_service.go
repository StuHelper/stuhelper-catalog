package catalog

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

type viewCounters struct{ recorded, duplicates, failed atomic.Uint64 }

// ViewMetrics exposes low-cardinality counters for the host metrics exporter.
type ViewMetrics struct{ Recorded, Duplicates, Failures uint64 }

func (s *Service) ViewMetrics() ViewMetrics {
	return ViewMetrics{s.viewStats.recorded.Load(), s.viewStats.duplicates.Load(), s.viewStats.failed.Load()}
}
func (s *Service) recordView(ctx context.Context, p Principal, o Object) {
	if p.Subject == "" || (o.Kind != "course" && o.Kind != "teacher") {
		return
	}
	call, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	added := false
	e := s.Repo.Transaction(call, func(t Transaction) error { var e error; added, e = t.RecordView(p.Subject, o, s.now()); return e })
	if e != nil {
		s.viewStats.failed.Add(1)
		logger := s.ActivityLogger
		if logger == nil {
			logger = slog.Default()
		}
		reason := "unavailable"
		if errors.Is(e, context.Canceled) {
			reason = "cancelled"
		}
		if errors.Is(e, context.DeadlineExceeded) {
			reason = "timeout"
		}
		logger.WarnContext(ctx, "catalog activity view write failed", "operation", "activity.view", "reason", reason)
		return
	}
	if added {
		s.viewStats.recorded.Add(1)
	} else {
		s.viewStats.duplicates.Add(1)
	}
}
func (s *Service) CurrentActivity(ctx context.Context, p Principal, id string) (Activity, bool, error) {
	var empty Activity
	target, _, e := ParseActivityID(id)
	if e != nil {
		return empty, false, e
	}
	object, e := s.readObject(ctx, p, target.ID)
	if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) {
		return empty, false, nil
	}
	if e != nil {
		return empty, false, e
	}
	if object.ID != target.ID || object.Kind != target.Kind || object.Inactive || object.CanonicalID != "" {
		return empty, false, nil
	}
	repo, ok := s.Repo.(ActivityReader)
	if !ok {
		return empty, false, ErrUnavailable
	}
	d, e := repo.ReadActivity(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return empty, false, nil
	}
	return d, e == nil, e
}
