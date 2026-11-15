package postgres

import (
	"context"
	"errors"
	catalog "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

func TestDirectoryDatabaseFailureCategoriesAreSanitized(t *testing.T) {
	for code, want := range map[string]string{"57014": "query_canceled", "53300": "connections_exhausted", "55P03": "lock_unavailable", "08006": "database_unavailable"} {
		err := safe(&pgconn.PgError{Code: code, Message: "synthetic-secret", Detail: "private-query-parameter"})
		var failure catalog.DirectoryFailure
		if !errors.As(err, &failure) || failure.Reason != want || !errors.Is(err, catalog.ErrUnavailable) {
			t.Fatalf("classification %s: %v", code, err)
		}
		if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
			t.Fatal("database details escaped")
		}
	}
	if !errors.Is(safe(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("deadline classification lost")
	}
}
