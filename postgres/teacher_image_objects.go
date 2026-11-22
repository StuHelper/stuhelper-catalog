package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func imageClass(k c.TeacherImageKey) string {
	if k.Kind == c.BodyImageObject {
		return "body"
	}
	return "avatars"
}
func lockImage(ctx context.Context, tx *sql.Tx, k c.TeacherImageKey) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,3))`, imageClass(k)+"/"+k.Digest)
	return safe(err)
}

// Commit the upload intent on a second connection before any external PUT.
// A failed SQL publication or process crash then remains discoverable without
// granting the runtime permission to enumerate a COS bucket.
func registerImage(ctx context.Context, tx *sql.Tx, db *sql.DB, k c.TeacherImageKey) error {
	if err := lockImage(ctx, tx, k); err != nil {
		return err
	}
	register, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := db.ExecContext(register, `INSERT INTO catalog.teacher_image_objects(class,digest) VALUES ($1,$2) ON CONFLICT DO NOTHING`, imageClass(k), k.Digest)
	return safe(err)
}
func (t *transaction) writeTeacherImage(k c.TeacherImageKey, data []byte, publish func([]byte) error) error {
	if _, external := t.teacherImageStorage.(c.TeacherImageObjects); external {
		if err := registerImage(t.ctx, t.tx, t.imageRegistryDB, k); err != nil {
			return err
		}
	}
	return t.imageStore().Write(t.ctx, k, data, publish)
}

// PrepareTeacherImage checks publication again under the per-object lock.
// Shared digests get a single edge copy regardless of the number of owners.
func (r *Repository) PrepareTeacherImage(ctx context.Context, k c.TeacherImageKey) error {
	store, ok := r.TeacherImageStorage.(c.TeacherImageObjects)
	if !ok {
		return c.ErrUnavailable
	}
	return r.Transaction(ctx, func(port c.Transaction) error {
		t := port.(*transaction)
		if err := lockImage(ctx, t.tx, k); err != nil {
			return err
		}
		visible, err := t.publicationVisible(k)
		if err != nil {
			return err
		}
		if !visible {
			return c.ErrNotFound
		}
		var exists bool
		err = t.tx.QueryRowContext(ctx, `SELECT has_delivery FROM catalog.teacher_image_objects WHERE class=$1 AND digest=$2`, imageClass(k), k.Digest).Scan(&exists)
		if err != nil {
			return safe(err)
		}
		if !exists {
			intent, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err = r.DB.ExecContext(intent, `UPDATE catalog.teacher_image_objects SET copy_pending=true WHERE class=$1 AND digest=$2`, imageClass(k), k.Digest)
			cancel()
			if err != nil {
				return safe(err)
			}
			if err := store.PublishCopy(ctx, k); err != nil {
				return err
			}
			if _, err = t.tx.ExecContext(ctx, `UPDATE catalog.teacher_image_objects SET has_delivery=true,copy_pending=false WHERE class=$1 AND digest=$2`, imageClass(k), k.Digest); err != nil {
				return safe(err)
			}
		}
		_, err = t.tx.ExecContext(ctx, `INSERT INTO catalog.teacher_image_publications(class,digest,kind,school,owner_id) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, imageClass(k), k.Digest, k.Kind, k.School, k.OwnerID)
		return safe(err)
	})
}
func (t *transaction) publicationVisible(k c.TeacherImageKey) (bool, error) {
	if k.Kind == c.RequestPortrait {
		var ok bool
		err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.teacher_homepage_request_photos p JOIN catalog.teacher_homepage_requests r ON r.id=p.request_id WHERE r.school_id=$1 AND r.id=$2 AND p.photo_id=$3 AND `+readableTeacherHomepageRequest+`)`, k.School, k.OwnerID, k.Digest).Scan(&ok)
		return ok, safe(err)
	}
	teacher, err := t.PublicObject(k.OwnerID)
	if errors.Is(err, c.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if teacher.Kind != "teacher" || teacher.SchoolID != k.School || teacher.Inactive {
		return false, nil
	}
	return c.TeacherImageIsPublished(t, teacher, k)
}

func (t *transaction) recheckTeacherImagePublications(school, owner string) error {
	if _, ok := t.teacherImageStorage.(c.TeacherImageObjects); !ok {
		return nil
	}
	_, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_image_publications SET next_check='-infinity'
 WHERE school=$1 AND ($2='' OR owner_id=$2) AND kind<>'request-portrait'`, school, owner)
	return safe(err)
}
