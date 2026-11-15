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

func TestDirectoryDiagnosticsKeepPhaseWithoutIdentifiers(t *testing.T) {
	var out bytes.Buffer
	s := &Service{DirectoryLogger: slog.New(slog.NewJSONHandler(&out, nil))}
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, DirectoryFailure{Reason: "query_canceled"}, DirectoryFailure{Reason: "connections_exhausted"}, DirectoryFailure{Reason: "synthetic-secret"}, errors.New("synthetic-private-filter")} {
		s.observeDirectory(context.Background(), "list", time.Now(), err)
	}
	text := out.String()
	for _, v := range []string{"list", "deadline_exceeded", "canceled", "query_canceled", "connections_exhausted", "unavailable"} {
		if !strings.Contains(text, v) {
			t.Fatal("missing bounded classification", v)
		}
	}
	if strings.Contains(text, "synthetic-") {
		t.Fatal("untrusted diagnostics leaked")
	}
	if !errors.Is(DirectoryFailure{Reason: "query_canceled"}, ErrUnavailable) {
		t.Fatal("public code changed")
	}
}
