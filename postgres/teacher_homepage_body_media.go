package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	c "github.com/StuHelper/stuhelper-catalog"
)

const teacherHomepageBodyImageBudget int64 = 2 << 30

var _ c.TeacherHomepageBodyMediaTransaction = (*transaction)(nil)

// verifiedBodyImage compares the stored image with the import's metadata. A
// site never inherits another image merely by carrying its digest.
const verifiedBodyImage = `item->>'kind'='image' AND i.image_id=item->'image'->>'id' AND i.media_type=item->'image'->>'content_type'
 AND i.width::text=item->'image'->>'width' AND i.height::text=item->'image'->>'height'
 AND i.size_bytes::text=item->'image'->>'size_bytes'`

const hasBodyImageRef = `SELECT 1 FROM catalog.teacher_homepage_body_refs r
 JOIN catalog.teacher_homepage_body_images i ON i.image_id=r.image_id AND ` + verifiedBodyImage + `
 WHERE r.school_id=s.school_id AND r.site_id=s.site_id AND r.source_url=s.url`

func (t *transaction) TeacherHomepageBodyMedia(school, teacherID string) ([]c.TeacherHomepageBodyMedia, error) {
	var raw []byte
	e := t.tx.QueryRowContext(t.ctx, `SELECT s.body_media`+homepageTeacherFrom()+homepageTeacherWhere(), school, teacherID).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, safe(e)
	}
	var items []c.TeacherHomepageBodyMedia
	if json.Unmarshal(raw, &items) != nil {
		return nil, c.ErrUnavailable
	}
	return items, nil
}

func (t *transaction) TeacherHomepageBodyImage(school, teacherID, imageID string, loadBytes bool) (c.TeacherHomepageImage, error) {
	var out c.TeacherHomepageImage
	columns := `i.image_id,i.media_type,i.width,i.height,i.size_bytes`
	args := []any{&out.ID, &out.MediaType, &out.Width, &out.Height, &out.SizeBytes}
	if loadBytes {
		columns += `,i.data`
		args = append(args, &out.Bytes)
	}
	err := t.tx.QueryRowContext(t.ctx, `SELECT `+columns+homepageTeacherFrom()+`
 JOIN catalog.teacher_homepage_body_refs r ON r.school_id=s.school_id AND r.site_id=s.site_id AND r.source_url=s.url
 JOIN catalog.teacher_homepage_body_images i ON i.image_id=r.image_id`+homepageTeacherWhere()+` AND i.image_id=$3`,
		school, teacherID, imageID).Scan(args...)
	return out, safe(err)
}

func (t *transaction) TeacherHomepageBodyImageQuotaExceeded() bool {
	return t.bodyImageQuotaExceeded
}

// Called under the shared image lock, after replacing site rows with the new
// metadata. Keep other schools' references and this school's still-needed
// copies. Reclaim unreferenced bytes before admitting any new copies. Every
// write shares the outer snapshot transaction, including the old refs' removal.
func (t *transaction) replaceTeacherHomepageBodyMedia(in c.TeacherSiteSnapshot, budget int64) error {
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_body_refs r USING catalog.teacher_homepage_sites s
 WHERE r.school_id=$1 AND s.school_id=r.school_id AND s.site_id=r.site_id AND (r.source_url<>s.url OR NOT EXISTS (
 SELECT 1 FROM jsonb_array_elements(s.body_media) b(item)
 JOIN catalog.teacher_homepage_body_images i ON `+verifiedBodyImage+`
 WHERE i.image_id=r.image_id))`, in.SchoolID); err != nil {
		return safe(err)
	}
	retained, err := t.retainedBodyImageRefs(in.SchoolID)
	if err != nil {
		return err
	}
	pending, err := t.attachStoredBodyImages(in, retained)
	if err != nil {
		return err
	}
	if err := t.storeNewBodyImages(in, pending, retained, budget); err != nil {
		return err
	}
	// Keep ordering and the original page link if the image itself failed
	// validation, was unavailable, or did not fit. Rewrite only affected sites.
	_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_homepage_sites s SET body_media=(
 SELECT jsonb_agg(CASE WHEN item ? 'image' AND NOT EXISTS (`+hasBodyImageRef+`)
 THEN item-'image' ELSE item END ORDER BY ordinality)
 FROM jsonb_array_elements(s.body_media) WITH ORDINALITY b(item,ordinality))
 WHERE s.school_id=$1 AND EXISTS (
 SELECT 1 FROM jsonb_array_elements(s.body_media) b(item) WHERE item ? 'image' AND NOT EXISTS (`+hasBodyImageRef+`))`, in.SchoolID)
	return safe(err)
}

type bodyImageSource struct {
	site, digest string
}

func (t *transaction) retainedBodyImageRefs(school string) (map[bodyImageSource]bool, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT site_id,image_id FROM catalog.teacher_homepage_body_refs WHERE school_id=$1`, school)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := make(map[bodyImageSource]bool)
	for rows.Next() {
		var key bodyImageSource
		if err := rows.Scan(&key.site, &key.digest); err != nil {
			return nil, safe(err)
		}
		out[key] = true
	}
	return out, safe(rows.Err())
}

type bodyImageReference struct {
	source bodyImageSource
	url    string
	image  c.TeacherHomepageBodyImage
}

type bodyImageCheck struct {
	stored, verified bool
}

// Establish genuine new references to existing blobs before deleting old
// orphans. A current candidate may use the same bytes at a different site,
// but possession of a digest alone is insufficient to give it that reference.
func (t *transaction) attachStoredBodyImages(in c.TeacherSiteSnapshot, retained map[bodyImageSource]bool) ([]bodyImageReference, error) {
	checked := make(map[c.TeacherHomepageBodyImage]bodyImageCheck)
	var pending []bodyImageReference
	for _, site := range in.Sites {
		for _, item := range site.BodyMedia {
			if item.Kind != "image" || item.Image == nil {
				continue
			}
			img := *item.Image
			source := bodyImageSource{site: site.SiteID, digest: img.ID}
			if retained[source] {
				continue
			}
			check, checkedImage := checked[img]
			if !checkedImage {
				var err error
				check, err = t.checkStoredBodyImage(in, img)
				if err != nil {
					return nil, err
				}
				checked[img] = check
			}
			ref := bodyImageReference{source: source, url: site.URL, image: img}
			if !check.stored {
				pending = append(pending, ref)
			} else if check.verified {
				if err := t.insertBodyImageRef(in.SchoolID, ref); err != nil {
					return nil, err
				}
				retained[source] = true
			}
		}
	}
	return pending, nil
}

func (t *transaction) checkStoredBodyImage(in c.TeacherSiteSnapshot, img c.TeacherHomepageBodyImage) (bodyImageCheck, error) {
	var check bodyImageCheck
	err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.teacher_homepage_body_images
 WHERE image_id=$1 AND media_type=$2 AND width=$3 AND height=$4 AND size_bytes=$5)`,
		img.ID, img.MediaType, img.Width, img.Height, img.SizeBytes).Scan(&check.stored)
	if err != nil {
		return check, safe(err)
	}
	if check.stored {
		bytes, err := t.readVerifiedBodyImage(in, img)
		check.verified = len(bytes) != 0
		return check, err
	}
	return check, nil
}

func (t *transaction) storeNewBodyImages(in c.TeacherSiteSnapshot, pending []bodyImageReference, retained map[bodyImageSource]bool, budget int64) error {
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_body_images i WHERE
 NOT EXISTS (SELECT 1 FROM catalog.teacher_homepage_body_refs r WHERE r.image_id=i.image_id)`); err != nil {
		return safe(err)
	}
	var used int64
	if err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(sum(size_bytes),0) FROM catalog.teacher_homepage_body_images`).Scan(&used); err != nil {
		return safe(err)
	}
	checked := make(map[c.TeacherHomepageBodyImage]bool)
	for _, ref := range pending {
		if retained[ref.source] {
			continue
		}
		img := ref.image
		stored, alreadyChecked := checked[img]
		if !alreadyChecked && !t.bodyImageQuotaExceeded {
			data, err := t.readVerifiedBodyImage(in, img)
			if err != nil {
				return err
			}
			if len(data) != 0 {
				if img.SizeBytes > budget-used {
					t.bodyImageQuotaExceeded = true
				} else if err := t.insertBodyImage(img, data); err != nil {
					return err
				} else {
					stored = true
					used += img.SizeBytes
				}
			}
			checked[img] = stored
		}
		if !stored {
			continue
		}
		if err := t.insertBodyImageRef(in.SchoolID, ref); err != nil {
			return err
		}
		retained[ref.source] = true
	}
	return nil
}

func (t *transaction) readVerifiedBodyImage(in c.TeacherSiteSnapshot, img c.TeacherHomepageBodyImage) ([]byte, error) {
	if in.ReadBodyImage == nil {
		return nil, nil
	}
	data, err := in.ReadBodyImage(t.ctx, img.ID)
	if canceled := t.ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || c.ValidateTeacherHomepageBodyImage(img, data) != nil {
		return nil, nil
	}
	return data, nil
}

func (t *transaction) insertBodyImage(img c.TeacherHomepageBodyImage, data []byte) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_body_images
 (image_id,media_type,width,height,size_bytes,data) VALUES($1,$2,$3,$4,$5,$6)`,
		img.ID, img.MediaType, img.Width, img.Height, img.SizeBytes, data)
	return safe(err)
}

func (t *transaction) insertBodyImageRef(school string, ref bodyImageReference) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_body_refs(school_id,site_id,image_id,source_url)
 VALUES($1,$2,$3,$4)`, school, ref.source.site, ref.source.digest, ref.url)
	return safe(err)
}
