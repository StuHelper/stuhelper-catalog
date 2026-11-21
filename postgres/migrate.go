package postgres

import (
	"context"
	"database/sql"
)

// Migrate applies the catalog schema and activity calendar as the migration identity. Statistics and
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

// RollbackActivityCalendar restores UTC buckets while retaining views recorded
// under the Beijing calendar. Quiesce application and relay writes during the
// cutover, and rebuild dependent projections before resuming service.
func RollbackActivityCalendar(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DO $$ BEGIN
 IF to_regprocedure('catalog.use_beijing_activity_days(boolean)') IS NOT NULL THEN
  PERFORM catalog.use_beijing_activity_days(false);
 END IF;
END $$`)
	return err
}
