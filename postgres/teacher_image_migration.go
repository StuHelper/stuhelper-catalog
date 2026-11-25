package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	c "github.com/StuHelper/stuhelper-catalog"
)

// Only these Catalog relations participate. Each locked row is its own
// checkpoint. A rollback reads current rows, including post-cutover uploads.
var imageRelations = []struct {
	table, digest string
	kind          c.TeacherImageKind
}{
	{"teacher_homepage_photos", "photo_id", c.SitePortrait},
	{"teacher_homepage_override_photos", "photo_id", c.OverridePortrait},
	{"teacher_homepage_request_photos", "photo_id", c.RequestPortrait},
	{"teacher_avatar_cache", "digest", c.DirectoryPortrait},
	{"teacher_homepage_body_images", "image_id", c.BodyImageObject},
}

func (r *Repository) MigrateTeacherImages(ctx context.Context, store c.TeacherImageStorage, restore bool, limit int) (int, error) {
	if store == nil || limit < 1 || limit > 1000 {
		return 0, c.ErrInput
	}
	count := 0
	for _, rel := range imageRelations {
		for count < limit {
			moved, err := r.migrateTeacherImage(ctx, store, restore, rel.table, rel.digest, rel.kind)
			if err != nil {
				return count, err
			}
			if !moved {
				break
			}
			count++
		}
	}
	return count, nil
}

func (r *Repository) migrateTeacherImage(ctx context.Context, store c.TeacherImageStorage, restore bool, table, digest string, kind c.TeacherImageKind) (bool, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, safe(err)
	}
	defer tx.Rollback()
	filter := "IS NOT NULL"
	if restore {
		filter = "IS NULL"
	}
	var tid string
	var k = c.TeacherImageKey{Kind: kind}
	var data []byte
	// ctid stays stable while FOR UPDATE holds the row. Re-evaluate the
	// predicate on each transaction instead of persisting physical row IDs.
	query := fmt.Sprintf("SELECT ctid::text,%s,data FROM catalog.%s WHERE data %s LIMIT 1 FOR UPDATE", digest, table, filter)
	err = tx.QueryRowContext(ctx, query).Scan(&tid, &k.Digest, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, safe(err)
	}
	if restore {
		data, err = store.Read(ctx, k, nil)
	} else {
		if _, ok := store.(c.TeacherImageObjects); ok {
			if err = registerImage(ctx, tx, r.DB, k); err != nil {
				return false, err
			}
		}
		err = store.Write(ctx, k, data, func(inline []byte) error {
			if inline != nil {
				return c.ErrInput
			}
			copy, e := store.Read(ctx, k, nil)
			if e != nil {
				return e
			}
			if !bytes.Equal(data, copy) {
				return c.ErrUnavailable
			}
			return nil
		})
		data = nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE catalog."+table+" SET data=$1 WHERE ctid=$2::tid", data, tid)
	if err != nil {
		return false, safe(err)
	}
	return true, safe(tx.Commit())
}
