package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

// Do not copy the profile into immutable directory generations, search
// documents, or list items. This lookup uses only current successful matches.
func homepageTeacherFrom() string {
	return ` FROM catalog.objects o
 JOIN catalog.teacher_directory td ON td.school_id=o.school_id AND td.teacher_id=o.id
 JOIN catalog.teacher_homepage_sites s ON s.school_id=td.school_id AND s.site_id=td.homepage_site_id`
}

func homepageTeacherWhere() string {
	return ` WHERE o.school_id=$1 AND o.id=$2 AND o.kind='teacher' AND o.id=catalog.canonical(o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND s.reason<>'site_unavailable'
 AND ` + officialTeacherCondition("td", "o")
}

const homepagePhotoJoin = `catalog.teacher_homepage_photos p ON p.school_id=s.school_id AND p.site_id=s.site_id AND p.photo_id=s.photo_id`

func (t *transaction) TeacherHomepage(school, teacherID string) (*c.TeacherHomepage, error) {
	var raw []byte
	var photoID, mediaType sql.NullString
	var width, height sql.NullInt32
	err := t.tx.QueryRowContext(t.ctx, `SELECT s.profile,p.photo_id,p.media_type,p.width,p.height`+
		homepageTeacherFrom()+` LEFT JOIN `+homepagePhotoJoin+homepageTeacherWhere(), school, teacherID).
		Scan(&raw, &photoID, &mediaType, &width, &height)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, safe(err)
	}
	var out c.TeacherHomepage
	if json.Unmarshal(raw, &out) != nil {
		return nil, c.ErrUnavailable
	}
	if photoID.Valid {
		out.Photo = &c.TeacherHomepagePhoto{
			ID: photoID.String, ContentType: mediaType.String, Width: int(width.Int32), Height: int(height.Int32),
		}
	}
	return &out, nil
}

func (t *transaction) TeacherHomepagePhoto(school, teacherID, photoID string) (c.TeacherHomepageImage, error) {
	var out c.TeacherHomepageImage
	err := t.tx.QueryRowContext(t.ctx, `SELECT p.photo_id,p.media_type,p.width,p.height,p.data`+
		homepageTeacherFrom()+` JOIN `+homepagePhotoJoin+homepageTeacherWhere()+` AND p.photo_id=$3`, school, teacherID, photoID).
		Scan(&out.ID, &out.MediaType, &out.Width, &out.Height, &out.Bytes)
	return out, safe(err)
}

func (t *transaction) SetTeacherSiteDecision(school, siteID string, in c.TeacherSiteDecisionInput, at time.Time) (c.TeacherSiteDecision, error) {
	var (
		out                   = c.TeacherSiteDecision{SiteID: siteID}
		name, unit            string
		oldAction, oldTeacher string
		present, listed       bool
	)
	err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE(s.name,d.name),COALESCE(s.unit,d.unit),
 COALESCE(s.reason<>'site_unavailable',false),s.site_id IS NOT NULL,COALESCE(d.revision,0),COALESCE(d.action,''),COALESCE(d.teacher_id,'')
 FROM (SELECT $1::text AS school_id,$2::text AS site_id) k
 LEFT JOIN catalog.teacher_homepage_sites s USING(school_id,site_id)
 LEFT JOIN catalog.teacher_homepage_decisions d USING(school_id,site_id)
 WHERE s.site_id IS NOT NULL OR d.site_id IS NOT NULL`, school, siteID).
		Scan(&name, &unit, &present, &listed, &out.DecisionRevision, &oldAction, &oldTeacher)
	if err != nil {
		return out, safe(err)
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision != out.DecisionRevision {
		return out, c.ErrConflict
	}
	if in.Action == "revoke" {
		if oldAction != "bind" && oldAction != "reject" {
			return out, c.ErrConflict
		}
	} else if !listed || in.Action == "bind" && !present {
		return out, c.ErrConflict
	}
	if in.Action == "bind" {
		if err := t.requireActiveHomepageTeacher(school, in.TeacherID); err != nil {
			return out, err
		}
	}
	if in.Action != "revoke" {
		out.Decision = &c.TeacherSiteDecisionValue{Action: in.Action, TeacherID: in.TeacherID}
	}
	if oldAction == in.Action && oldTeacher == in.TeacherID {
		return out, nil
	}
	out.DecisionRevision++
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_decisions
 (school_id,site_id,revision,action,teacher_id,name,unit,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT(school_id,site_id) DO UPDATE SET revision=excluded.revision,action=excluded.action,teacher_id=excluded.teacher_id,
 name=excluded.name,unit=excluded.unit,updated_at=excluded.updated_at`, school, siteID, out.DecisionRevision, in.Action, in.TeacherID, name, unit, at); err != nil {
		return out, safe(err)
	}
	if err := t.bumpHomepageReview(school); err != nil {
		return out, err
	}
	changed, err := t.applyHomepageMembership(school, at)
	if err != nil {
		return out, err
	}
	if len(changed) == 0 {
		return out, nil
	}
	if err := t.syncTeacherDiscoveryAndActivity(school, changed, at); err != nil {
		return out, err
	}
	// A projection build may already be reading a committed set of marks. Its
	// pending watermark must not clear this later decision. The school lock
	// still serializes this decision with catalog and site snapshot writers.
	return out, t.markProjectionPending(school)
}

func (t *transaction) requireActiveHomepageTeacher(school, teacher string) error {
	var exists bool
	err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects o
 WHERE o.school_id=$1 AND o.id=$2 AND o.kind='teacher' AND o.id=catalog.canonical(o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false))`, school, teacher).Scan(&exists)
	if err != nil {
		return safe(err)
	}
	if !exists {
		return c.ErrNotFound
	}
	return nil
}
