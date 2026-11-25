package postgres

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (r *Repository) CachedTeacherAvatar(ctx context.Context, source string, loadBytes bool) (c.TeacherHomepageImage, bool, error) {
	out := c.TeacherHomepageImage{MediaType: "image/jpeg", Width: c.TeacherAvatarSize, Height: c.TeacherAvatarSize}
	err := r.DB.QueryRowContext(ctx, `SELECT digest,CASE WHEN $2 THEN data END FROM catalog.teacher_avatar_cache WHERE source_digest=$1`, source, loadBytes).Scan(&out.ID, &out.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, safe(err)
	}
	if loadBytes && len(out.Bytes) == 0 {
		if r.TeacherImageStorage == nil {
			return out, false, c.ErrUnavailable
		}
		out.Bytes, err = r.TeacherImageStorage.Read(ctx, c.TeacherImageKey{Kind: c.DirectoryPortrait, Digest: out.ID}, func() ([]byte, error) { return nil, c.ErrUnavailable })
	}
	return out, true, err
}

func (r *Repository) CacheTeacherAvatar(ctx context.Context, source string, thumb c.TeacherHomepageImage) error {
	return r.Transaction(ctx, func(port c.Transaction) error {
		t := port.(*transaction)
		// Only cold inserts take this lock. The default READ COMMITTED isolation
		// lets the next writer see the preceding writer's capacity decision.
		if err := t.Lock("teacher-avatar-cache"); err != nil {
			return err
		}
		var exists bool
		if err := t.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog.teacher_avatar_cache WHERE source_digest=$1)`, source).Scan(&exists); err != nil {
			return safe(err)
		}
		if exists {
			return nil
		}
		_, err := t.tx.ExecContext(ctx, `DELETE FROM catalog.teacher_avatar_cache WHERE source_digest=(
 SELECT source_digest FROM catalog.teacher_avatar_cache ORDER BY created_at,source_digest LIMIT 1)
 AND (SELECT count(*) FROM catalog.teacher_avatar_cache)>=$1`, c.TeacherAvatarCacheEntries)
		if err != nil {
			return safe(err)
		}
		return t.writeTeacherImage(c.TeacherImageKey{Kind: c.DirectoryPortrait, Digest: thumb.ID}, thumb.Bytes, func(inline []byte) error {
			_, err := t.tx.ExecContext(ctx, `INSERT INTO catalog.teacher_avatar_cache(source_digest,digest,data) VALUES($1,$2,$3)`, source, thumb.ID, inline)
			return safe(err)
		})
	})
}
