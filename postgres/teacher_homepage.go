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
	var site string
	err := t.tx.QueryRowContext(t.ctx, `SELECT p.photo_id,p.media_type,p.width,p.height,p.site_id`+
		homepageTeacherFrom()+` JOIN `+homepagePhotoJoin+homepageTeacherWhere()+` AND p.photo_id=$3`, school, teacherID, photoID).
		Scan(&out.ID, &out.MediaType, &out.Width, &out.Height, &site)
	if err != nil {
		return out, safe(err)
	}
	return t.readImage(c.TeacherImageKey{Kind: c.SitePortrait, School: school, OwnerID: site, Digest: out.ID}, out)
}

func (t *transaction) TeacherHomepageSourceRevision(school, teacherID string) (string, int64, error) {
	var siteID string
	var revision int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT s.site_id,s.revision`+
		homepageTeacherFrom()+homepageTeacherWhere(), school, teacherID).Scan(&siteID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return siteID, revision, safe(err)
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
	return out, t.markHomepageProjectionPending(school, changed)
}

func (t *transaction) markHomepageProjectionPending(school string, changed []string) error {
	// Only these teachers can have lost the evidence used by an in-flight
	// generation. A later decision can remove its restored IDs from the set.
	result, err := t.tx.ExecContext(t.ctx, `WITH current AS (
 SELECT changed.id,
 COALESCE(o.id=catalog.canonical(o.id) AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false),false) AS active,
 COALESCE((`+homepageTeacherEvidence("td", "o")+`),false) AS homepage,
 COALESCE(td.verified_homepage,false) AS independently_approved
 FROM unnest($2::text[]) changed(id)
 LEFT JOIN catalog.objects o ON o.id=changed.id AND o.school_id=$1
 LEFT JOIN catalog.teacher_directory td ON td.teacher_id=o.id AND td.school_id=$1
 )
 INSERT INTO catalog.projection_pending AS pending(school_id,requested_at,updated_at,homepage_revoked_ids,verified_teachers_unavailable_ids)
 SELECT $1,clock_timestamp(),clock_timestamp(),ARRAY(SELECT id FROM current WHERE NOT(active AND homepage)),
 ARRAY(SELECT id FROM current WHERE independently_approved AND NOT active)
 WHERE EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)
 ON CONFLICT(school_id) DO UPDATE SET
 updated_at=GREATEST(excluded.updated_at,pending.updated_at + interval '1 microsecond'),
 homepage_revoked_ids=ARRAY(
  SELECT id FROM unnest(pending.homepage_revoked_ids) kept(id) WHERE id<>ALL($2::text[])
  UNION ALL SELECT unnest(excluded.homepage_revoked_ids)
 ),
 verified_teachers_unavailable_ids=ARRAY(
  SELECT id FROM unnest(pending.verified_teachers_unavailable_ids) kept(id) WHERE id<>ALL($2::text[])
  UNION ALL SELECT unnest(excluded.verified_teachers_unavailable_ids)
 )`, school, changed)
	if err != nil {
		return safe(err)
	}
	written, err := result.RowsAffected()
	if err != nil || written == 0 {
		return safe(err)
	}
	_, err = t.tx.ExecContext(t.ctx, `SELECT pg_notify($1,$2)`, c.ProjectionChannel, school)
	return safe(err)
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
