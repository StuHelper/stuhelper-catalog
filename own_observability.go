package catalog

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Diagnostics contain only fixed operation/reason labels and elapsed time.
// Neither SQL errors nor account, course and enrollment identifiers are logged.
func (s *Service) observeOwn(ctx context.Context, operation string, start time.Time, err error) {
	elapsed := time.Since(start)
	if err == nil && elapsed < 300*time.Millisecond {
		return
	}
	reason := "slow"
	switch {
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	case errors.Is(err, ErrConflict):
		reason = "conflict"
	case errors.Is(err, ErrForbidden):
		reason = "forbidden"
	case errors.Is(err, ErrInput):
		reason = "invalid_input"
	case err != nil:
		reason = "unavailable"
	}
	logger := s.OwnLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "catalog own request phase", "operation", operation, "reason", reason, "duration_ms", elapsed.Milliseconds())
}
