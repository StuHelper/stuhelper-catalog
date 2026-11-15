package postgres

import (
	"context"
	"database/sql"
)

// Migrate applies additive catalog DDL as the migration identity. Statistics and
// the visibility map are refreshed after commit so the covering indexes can be
// used on an existing directory. A canceled maintenance step can be retried:
// business rows are retained and Schema is idempotent.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, Schema); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `VACUUM (ANALYZE) catalog.objects`)
	return err
}
