package catalog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestOwnDiagnosticsKeepPhaseAndRemoveSensitiveErrors(t *testing.T) {
	var output bytes.Buffer
	s := &Service{OwnLogger: slog.New(slog.NewJSONHandler(&output, nil))}
	s.observeOwn(context.Background(), "schedule.meetings", time.Now(), context.DeadlineExceeded)
	s.observeOwn(context.Background(), "snapshot.source", time.Now(), errors.New("synthetic-private-account-and-credential"))
	text := output.String()
	if !strings.Contains(text, "schedule.meetings") || !strings.Contains(text, "deadline_exceeded") || !strings.Contains(text, "unavailable") {
		t.Fatal("missing useful phase diagnostics")
	}
	if strings.Contains(text, "synthetic-private") {
		t.Fatal("untrusted error exposed private material")
	}
}
