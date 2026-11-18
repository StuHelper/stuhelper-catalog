package catalog

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DirectoryFailure records a bounded diagnostic category while preserving the
// public unavailable code. The cause is deliberately omitted: drivers may put
// identifiers, statement parameters or connection details in error messages.
type DirectoryFailure struct{ Reason string }

func (e DirectoryFailure) Error() string { return ErrUnavailable.Error() }
func (e DirectoryFailure) Unwrap() error { return ErrUnavailable }

func (s *Service) observeDirectory(ctx context.Context, phase string, start time.Time, err error) {
	elapsed := time.Since(start)
	if err == nil && elapsed < 300*time.Millisecond {
		return
	}
	reason := "slow"
	var failure DirectoryFailure
	switch {
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	case errors.As(err, &failure):
		switch failure.Reason {
		case "query_canceled", "connections_exhausted", "lock_unavailable", "database_unavailable":
			reason = failure.Reason
		default:
			reason = "unavailable"
		}
	case errors.Is(err, ErrForbidden):
		reason = "forbidden"
	case err != nil:
		reason = "unavailable"
	}
	logger := s.DirectoryLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "catalog directory request phase", "phase", phase, "reason", reason, "duration_ms", elapsed.Milliseconds())
}

// DirectoryCacheMetrics exposes bounded in-process cache counters for host exporters.
type DirectoryCacheMetrics struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
	Entries   int    `json:"entries"`
	Bytes     int    `json:"bytes"`
}

type SchoolProjectionMetrics struct {
	SchoolID          string     `json:"school_id"`
	Version           int64      `json:"version,omitempty"`
	BuiltAt           *time.Time `json:"built_at,omitempty"`
	AgeSeconds        int64      `json:"age_seconds"`
	LastFailureReason string     `json:"last_failure_reason,omitempty"`
	LastFailedAt      *time.Time `json:"last_failed_at,omitempty"`
}

type OperationalMetrics struct {
	Views         ViewMetrics               `json:"views"`
	Cache         DirectoryCacheMetrics     `json:"cache"`
	PendingOutbox int64                     `json:"pending_outbox"`
	Projections   []SchoolProjectionMetrics `json:"projections"`
}

func (s *Service) DirectoryCacheMetrics() DirectoryCacheMetrics {
	port, ok := s.Repo.(interface {
		DirectoryCacheMetrics() DirectoryCacheMetrics
	})
	if !ok {
		return DirectoryCacheMetrics{}
	}
	return port.DirectoryCacheMetrics()
}

func (s *Service) OperationalMetrics(ctx context.Context) (OperationalMetrics, error) {
	port, ok := s.Repo.(interface {
		OperationalMetrics(context.Context, time.Time) (OperationalMetrics, error)
	})
	if !ok {
		return OperationalMetrics{}, ErrUnavailable
	}
	out, err := port.OperationalMetrics(ctx, s.now())
	if err != nil {
		return OperationalMetrics{}, err
	}
	out.Views = s.ViewMetrics()
	return out, nil
}

func (s *Service) PruneSentOutbox(ctx context.Context, maxAge time.Duration, limit int) (int64, error) {
	if maxAge < 0 || limit < 1 || limit > 1000 {
		return 0, ErrInput
	}
	port, ok := s.Repo.(interface {
		PruneSentOutbox(context.Context, time.Time, int) (int64, error)
	})
	if !ok {
		return 0, ErrUnavailable
	}
	return port.PruneSentOutbox(ctx, s.now().Add(-maxAge), limit)
}

func (s *Service) PruneReplays(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInput
	}
	port, ok := s.Repo.(interface {
		PruneReplays(context.Context, time.Time, int) (int64, error)
	})
	if !ok {
		return 0, ErrUnavailable
	}
	return port.PruneReplays(ctx, s.now(), limit)
}

func (s *Service) PruneTeacherRefreshRuns(ctx context.Context, maxAge time.Duration, limit int) (int64, error) {
	if maxAge < 0 || limit < 1 || limit > 1000 {
		return 0, ErrInput
	}
	port, ok := s.Repo.(interface {
		PruneTeacherRefreshRuns(context.Context, time.Time, int) (int64, error)
	})
	if !ok {
		return 0, ErrUnavailable
	}
	return port.PruneTeacherRefreshRuns(ctx, s.now().Add(-maxAge), limit)
}
