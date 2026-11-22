package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	c "github.com/StuHelper/stuhelper-catalog"
)

// InlineTeacherImages keeps publication and the existing bytea write atomic.
type InlineTeacherImages struct{}

func (InlineTeacherImages) Write(_ context.Context, _ c.TeacherImageKey, data []byte, publish func([]byte) error) error {
	return publish(data)
}

func (InlineTeacherImages) Read(_ context.Context, _ c.TeacherImageKey, readInline func() ([]byte, error)) ([]byte, error) {
	return readInline()
}

func (t *transaction) imageStore() c.TeacherImageStorage {
	if t.teacherImageStorage != nil {
		return t.teacherImageStorage
	}
	return InlineTeacherImages{}
}

func (t *transaction) loadImageBytes(k c.TeacherImageKey) ([]byte, error) {
	return t.imageStore().Read(t.ctx, k, func() ([]byte, error) {
		var query string
		args := []any{k.School, k.OwnerID, k.Digest}
		switch k.Kind {
		case c.BodyImageObject:
			query = "SELECT data FROM catalog.teacher_homepage_body_images WHERE image_id=$1"
			args = []any{k.Digest}
		case c.SitePortrait:
			query = "SELECT data FROM catalog.teacher_homepage_photos WHERE school_id=$1 AND site_id=$2 AND photo_id=$3"
		case c.OverridePortrait:
			query = "SELECT data FROM catalog.teacher_homepage_override_photos WHERE school_id=$1 AND teacher_id=$2 AND photo_id=$3"
		case c.RequestPortrait:
			query = `SELECT p.data FROM catalog.teacher_homepage_request_photos p JOIN catalog.teacher_homepage_requests r ON r.id=p.request_id
 WHERE r.school_id=$1 AND p.request_id=$2 AND p.photo_id=$3`
		default:
			return nil, c.ErrInput
		}
		var data []byte
		if err := t.tx.QueryRowContext(t.ctx, query, args...).Scan(&data); err != nil {
			return nil, safe(err)
		}
		if len(data) == 0 {
			return nil, c.ErrUnavailable
		}
		return data, nil
	})
}

func (t *transaction) readImage(k c.TeacherImageKey, image c.TeacherHomepageImage) (c.TeacherHomepageImage, error) {
	data, err := t.loadImageBytes(k)
	if err != nil {
		return c.TeacherHomepageImage{}, err
	}
	limit := 2 << 20
	if k.Kind == c.BodyImageObject {
		limit = c.TeacherHomepageMaxBodyImageBytes
	}
	if len(data) == 0 || len(data) > limit {
		return c.TeacherHomepageImage{}, c.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != k.Digest {
		return c.TeacherHomepageImage{}, c.ErrUnavailable
	}
	image.Bytes = data
	return image, nil
}
