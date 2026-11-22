package postgres

import (
	"database/sql"
	"encoding/json"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

const activeHomepageControlsTeacher = `o.school_id=$1 AND o.id=$2 AND o.kind='teacher' AND o.id=catalog.canonical(o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)`

const homepageControlsTeacher = ` FROM catalog.objects o
 LEFT JOIN catalog.teacher_homepage_controls saved ON saved.school_id=o.school_id AND saved.teacher_id=o.id`

func (t *transaction) TeacherHomepageControls(school, teacherID string) (c.TeacherHomepageControls, error) {
	var out c.TeacherHomepageControls
	var rules []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(saved.revision,0),COALESCE(saved.page_visibility,'visible'),
 COALESCE(td.verified_homepage,false),COALESCE(saved.rules,'[]'::jsonb)`+homepageControlsTeacher+`
 LEFT JOIN catalog.teacher_directory td ON td.school_id=o.school_id AND td.teacher_id=o.id
 WHERE `+activeHomepageControlsTeacher, school, teacherID).
		Scan(&out.Revision, &out.PageVisibility, &out.Independent, &rules)
	if err != nil {
		return out, safe(err)
	}
	if err := json.Unmarshal(rules, &out.Rules); err != nil {
		return c.TeacherHomepageControls{}, c.ErrUnavailable
	}
	return out, nil
}

func (t *transaction) SaveTeacherHomepageControls(school, teacherID string, in c.TeacherHomepageControls, at time.Time) error {
	if in.Revision < 1 {
		return c.ErrInput
	}
	for _, rule := range in.Rules {
		if rule.Override != nil && rule.Override.Photo != nil && len(rule.Override.Photo.Data) != 0 {
			return c.ErrInput
		}
	}
	rules := in.Rules
	if rules == nil {
		rules = []c.TeacherHomepageFieldRule{}
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return c.ErrInput
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_controls AS publication
 (school_id,teacher_id,revision,page_visibility,rules,updated_at)
 SELECT o.school_id,o.id,$3,$4,$5::jsonb,$6`+homepageControlsTeacher+`
 WHERE `+activeHomepageControlsTeacher+`
 AND COALESCE(saved.revision,0)=$3::bigint-1
 ON CONFLICT(school_id,teacher_id) DO UPDATE SET revision=excluded.revision,page_visibility=excluded.page_visibility,
 rules=excluded.rules,updated_at=excluded.updated_at
 WHERE publication.revision=excluded.revision-1`,
		school, teacherID, in.Revision, in.PageVisibility, string(raw), at)
	return teacherHomepageWriteResult(res, err)
}

const homepageControlsSchool = ` FROM catalog.objects o
 LEFT JOIN catalog.teacher_homepage_allowlists saved ON saved.school_id=o.id
 WHERE o.id=$1 AND o.kind='school' AND o.id=catalog.canonical(o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)`

func (t *transaction) TeacherHomepageAllowlist(school string) (c.TeacherHomepageAllowlist, error) {
	var out c.TeacherHomepageAllowlist
	var fields []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(saved.revision,0),
 COALESCE(saved.fields,'[]'::jsonb)`+homepageControlsSchool, school).Scan(&out.Revision, &fields)
	if err != nil {
		return out, safe(err)
	}
	if err := json.Unmarshal(fields, &out.Fields); err != nil {
		return c.TeacherHomepageAllowlist{}, c.ErrUnavailable
	}
	return out, nil
}

func (t *transaction) SaveTeacherHomepageAllowlist(school string, in c.TeacherHomepageAllowlist, at time.Time) error {
	if in.Revision < 1 {
		return c.ErrInput
	}
	fields := in.Fields
	if fields == nil {
		fields = []c.TeacherHomepageFieldSelector{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return c.ErrInput
	}
	res, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_allowlists AS allowlist
 (school_id,revision,fields,updated_at) SELECT o.id,$2,$3::jsonb,$4`+homepageControlsSchool+`
 AND COALESCE(saved.revision,0)=$2::bigint-1
 ON CONFLICT(school_id) DO UPDATE SET revision=excluded.revision,fields=excluded.fields,updated_at=excluded.updated_at
 WHERE allowlist.revision=excluded.revision-1`, school, in.Revision, string(raw), at)
	return teacherHomepageWriteResult(res, err)
}

func (t *transaction) TeacherHomepageOverridePhoto(school, teacherID, photoID string) (c.TeacherHomepageImage, error) {
	var out c.TeacherHomepageImage
	err := t.tx.QueryRowContext(t.ctx, `SELECT p.photo_id,p.media_type,p.width,p.height
 FROM catalog.objects o JOIN catalog.teacher_homepage_override_photos p ON p.school_id=o.school_id AND p.teacher_id=o.id
 WHERE `+activeHomepageControlsTeacher+` AND p.photo_id=$3`, school, teacherID, photoID).
		Scan(&out.ID, &out.MediaType, &out.Width, &out.Height)
	if err != nil {
		return out, safe(err)
	}
	return t.readImage(c.TeacherImageKey{Kind: c.OverridePortrait, School: school, OwnerID: teacherID, Digest: out.ID}, out)
}

func (t *transaction) SaveTeacherHomepageOverridePhoto(school, teacherID string, photo c.TeacherHomepageImage) error {
	return t.imageStore().Write(t.ctx, c.TeacherImageKey{Kind: c.OverridePortrait, School: school, OwnerID: teacherID, Digest: photo.ID}, photo.Bytes, func(inline []byte) error {
		res, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_override_photos
 (school_id,teacher_id,photo_id,media_type,width,height,data) SELECT o.school_id,o.id,$3,$4,$5,$6,$7
 FROM catalog.objects o WHERE `+activeHomepageControlsTeacher+`
 ON CONFLICT(school_id,teacher_id) DO UPDATE SET
 photo_id=excluded.photo_id,media_type=excluded.media_type,width=excluded.width,height=excluded.height,data=excluded.data`,
			school, teacherID, photo.ID, photo.MediaType, photo.Width, photo.Height, inline)
		return teacherHomepageWriteResult(res, err)
	})
}

func (t *transaction) DeleteTeacherHomepageOverridePhoto(school, teacherID string) error {
	_, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_override_photos WHERE school_id=$1 AND teacher_id=$2`, school, teacherID)
	return safe(err)
}

func teacherHomepageWriteResult(res sql.Result, err error) error {
	if err != nil {
		return safe(err)
	}
	written, err := res.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if written != 1 {
		return c.ErrConflict
	}
	return nil
}
