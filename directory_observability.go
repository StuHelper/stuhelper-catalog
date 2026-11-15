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
