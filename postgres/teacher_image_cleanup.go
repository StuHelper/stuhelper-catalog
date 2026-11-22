package postgres

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/StuHelper/stuhelper-catalog"
)

// ReconcileTeacherImages revisits both live publication and storage references.
// Successful items move to the back of the queue. Each item commits separately.
func (r *Repository) ReconcileTeacherImages(ctx context.Context, limit int) (int, error) {
	store, ok := r.TeacherImageStorage.(c.TeacherImageObjects)
	if !ok || limit < 1 || limit > 1000 {
		return 0, c.ErrInput
	}
	if _, err := r.DB.ExecContext(ctx, pruneImageCounters, 2*limit); err != nil {
		return 0, safe(err)
	}
	done := 0
	for i := 0; i < limit; i++ {
		links, objects := false, false
		err := r.Transaction(ctx, func(port c.Transaction) error {
			var e error
			links, e = port.(*transaction).checkPublication()
			return e
		})
		if err != nil {
			return done, err
		}
		err = r.Transaction(ctx, func(port c.Transaction) error {
			var e error
			objects, e = port.(*transaction).checkImageObject(store)
			return e
		})
		if err != nil {
			return done, err
		}
		if !links && !objects {
			break
		}
		done++
	}
	return done, nil
}
func (t *transaction) checkPublication() (bool, error) {
	var k c.TeacherImageKey
	row := t.tx.QueryRowContext(t.ctx, `SELECT kind,school,owner_id,digest FROM catalog.teacher_image_publications WHERE next_check<=now() ORDER BY next_check LIMIT 1`)
	if err := row.Scan(&k.Kind, &k.School, &k.OwnerID, &k.Digest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, safe(err)
	}
	if err := lockImage(t.ctx, t.tx, k); err != nil {
		return false, err
	}
	visible, err := t.publicationVisible(k)
	if err != nil {
		return false, err
	}
	args := []any{imageClass(k), k.Digest, k.Kind, k.School, k.OwnerID}
	const pred = ` WHERE class=$1 AND digest=$2 AND kind=$3 AND school=$4 AND owner_id=$5`
	if visible {
		_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_image_publications SET next_check=now()+interval '60 seconds'`+pred, args...)
	} else {
		_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_image_publications`+pred, args...)
		if err == nil {
			_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_image_objects SET next_check=now() WHERE class=$1 AND digest=$2`, args[:2]...)
		}
	}
	return true, safe(err)
}
func (t *transaction) checkImageObject(store c.TeacherImageObjects) (bool, error) {
	var class string
	var k c.TeacherImageKey
	err := t.tx.QueryRowContext(t.ctx, `SELECT class,digest FROM catalog.teacher_image_objects WHERE next_check<=now() ORDER BY next_check LIMIT 1`).Scan(&class, &k.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, safe(err)
	}
	k.Kind = c.SitePortrait
	if class == "body" {
		k.Kind = c.BodyImageObject
	}
	if err = lockImage(t.ctx, t.tx, k); err != nil {
		return false, err
	}
	var published, pending, links bool
	args := []any{class, k.Digest}
	err = t.tx.QueryRowContext(t.ctx, `SELECT has_delivery,copy_pending,EXISTS(SELECT 1 FROM catalog.teacher_image_publications p WHERE p.class=o.class AND p.digest=o.digest)
 FROM catalog.teacher_image_objects o WHERE class=$1 AND digest=$2`, args...).Scan(&published, &pending, &links)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, safe(err)
	}
	var retained bool
	const avatar = `SELECT EXISTS(SELECT 1 FROM catalog.teacher_homepage_photos WHERE photo_id=$1 AND data IS NULL
 UNION ALL SELECT 1 FROM catalog.teacher_homepage_override_photos WHERE photo_id=$1 AND data IS NULL
 UNION ALL SELECT 1 FROM catalog.teacher_homepage_request_photos WHERE photo_id=$1 AND data IS NULL)`
	query := avatar
	if class == "body" {
		query = `SELECT EXISTS(SELECT 1 FROM catalog.teacher_homepage_body_images WHERE image_id=$1 AND data IS NULL)`
	}
	if err = t.tx.QueryRowContext(t.ctx, query, k.Digest).Scan(&retained); err != nil {
		return false, safe(err)
	}
	if (!links || !retained) && (published || pending) {
		if err = store.RemoveObject(t.ctx, k, false); err != nil {
			return false, err
		}
		_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_image_purges(class,digest) VALUES($1,$2) ON CONFLICT (class,digest) DO UPDATE SET queued_at=now(),job_id=''`, args...)
		if err != nil {
			return false, safe(err)
		}
		published = false
	}
	if retained {
		_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_image_objects SET next_check=now()+interval '60 seconds',has_delivery=$3,copy_pending=false WHERE class=$1 AND digest=$2`, class, k.Digest, published)
	} else {
		if err = store.RemoveObject(t.ctx, k, true); err != nil {
			return false, err
		}
		_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_image_objects WHERE class=$1 AND digest=$2`, args...)
	}
	return true, safe(err)
}

// TeacherImagePurger reports completion separately from submission so a process
// restart or temporary provider outage can continue polling the original job.
type TeacherImagePurger interface {
	Submit(context.Context, []string) (string, error)
	Status(ctx context.Context, job string, paths []string) (complete, failed bool, err error)
}

// FlushTeacherImagePurges performs one bounded remote call. A transient
// status error leaves the saved job ID intact. Re-invalidating a republished
// object queues a fresh task even if an older task is still processing.
func (r *Repository) FlushTeacherImagePurges(ctx context.Context, limit int, purger TeacherImagePurger) (int, error) {
	if limit < 1 || limit > 100 || purger == nil {
		return 0, c.ErrInput
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, safe(err)
	}
	defer tx.Rollback()
	var job string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM catalog.teacher_image_purges ORDER BY queued_at LIMIT 1`).Scan(&job)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, safe(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT class||'/'||digest FROM catalog.teacher_image_purges WHERE job_id=$1 ORDER BY queued_at,class,digest LIMIT $2 FOR UPDATE SKIP LOCKED`, job, limit)
	if err != nil {
		return 0, safe(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return 0, safe(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, safe(err)
	}
	if len(names) == 0 {
		return 0, nil
	}
	var complete, failed bool
	if job == "" {
		job, err = purger.Submit(ctx, names)
		if err == nil && job == "" {
			return 0, c.ErrUnavailable
		}
	} else {
		complete, failed, err = purger.Status(ctx, job, names)
	}
	if err != nil {
		return 0, err
	}
	if complete {
		_, err = tx.ExecContext(ctx, `DELETE FROM catalog.teacher_image_purges WHERE class||'/'||digest=ANY($1)`, names)
	} else {
		if failed {
			job = ""
		}
		_, err = tx.ExecContext(ctx, `UPDATE catalog.teacher_image_purges SET job_id=$2,queued_at=now() WHERE class||'/'||digest=ANY($1)`, names, job)
	}
	if err != nil {
		return 0, safe(err)
	}
	if err = tx.Commit(); err != nil {
		return 0, safe(err)
	}
	if failed {
		return 0, c.ErrUnavailable
	}
	return len(names), nil
}
