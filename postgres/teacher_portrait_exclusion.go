package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5"
)

//go:embed teacher_portrait_exclusion.sql
var portraitExclusionSchema string

// InstallSitePortraitPolicy installs only this migration on an existing Catalog.
// The migration identity grants access before the trigger becomes visible.
func InstallSitePortraitPolicy(ctx context.Context, db *sql.DB, runtimeRole string) error {
	if !c.ValidID(runtimeRole) || len(runtimeRole) > 63 {
		return c.ErrInput
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='3s'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, portraitExclusionSchema); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "GRANT SELECT, INSERT ON catalog.teacher_portrait_exclusions TO "+pgx.Identifier{runtimeRole}.Sanitize()); err != nil {
		return err
	}
	return tx.Commit()
}

func (t *transaction) ExcludeSitePortrait(school, digest string, sites []string, actor string, at time.Time) error {
	var same bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT site_ids=$3::text[] FROM catalog.teacher_portrait_exclusions WHERE school_id=$1 AND digest=$2`, school, digest, sites).Scan(&same)
	if err == nil {
		if same {
			return nil
		}
		return c.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return safe(err)
	}
	args := []any{school, digest, sites}
	var exact bool
	err = t.tx.QueryRowContext(t.ctx, `SELECT
 ARRAY(SELECT site_id FROM catalog.teacher_homepage_sites WHERE school_id=$1 AND photo_id=$2 ORDER BY site_id COLLATE "C")=$3::text[]
 AND ARRAY(SELECT site_id FROM catalog.teacher_homepage_photos WHERE school_id=$1 AND photo_id=$2 ORDER BY site_id COLLATE "C")=$3::text[]`, args...).Scan(&exact)
	if err != nil {
		return safe(err)
	}
	if !exact {
		return c.ErrConflict
	}
	if _, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_portrait_exclusions(school_id,digest,site_ids) VALUES($1,$2,$3)`, args...); err != nil {
		return safe(err)
	}
	for _, query := range []string{
		`UPDATE catalog.teacher_homepage_sites SET photo_id='' WHERE school_id=$1 AND photo_id=$2 AND site_id=ANY($3)`,
		`DELETE FROM catalog.teacher_homepage_photos WHERE school_id=$1 AND photo_id=$2 AND site_id=ANY($3)`,
	} {
		res, err := t.tx.ExecContext(t.ctx, query, args...)
		if err != nil {
			return safe(err)
		}
		count, err := res.RowsAffected()
		if err != nil {
			return safe(err)
		}
		if count != int64(len(sites)) {
			return c.ErrConflict
		}
	}
	for _, site := range sites {
		if err := t.recordAudit(c.Event{ID: "portrait-exclusion:" + rand.Text(), Type: "catalog.teacher-portrait.excluded.v1", Subject: "teacher-site/" + site, SchoolID: school, Actor: actor, Time: at, Result: "success"}); err != nil {
			return err
		}
	}
	return nil
}
