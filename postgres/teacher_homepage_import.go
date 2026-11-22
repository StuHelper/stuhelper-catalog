package postgres

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

type homepageSiteRow struct {
	SiteID       string                       `json:"site_id"`
	Name         string                       `json:"name"`
	Unit         string                       `json:"unit"`
	URL          string                       `json:"url"`
	Reason       string                       `json:"reason"`
	CandidateIDs []string                     `json:"candidate_ids"`
	Count        int64                        `json:"candidate_count"`
	Profile      c.TeacherHomepage            `json:"profile"`
	BodyMedia    []c.TeacherHomepageBodyMedia `json:"body_media"`
	PhotoID      string                       `json:"photo_id"`
	AutoTeacher  string                       `json:"auto_teacher_id"`
	AutoName     string                       `json:"auto_name"`
	AutoDept     string                       `json:"auto_department_id"`
}

func (t *transaction) replaceTeacherHomepageSnapshot(in c.TeacherSiteSnapshot, at time.Time) error {
	if err := t.saveTeacherHomepageSites(in, teacherHomepageBodyImageBudget); err != nil {
		return err
	}
	if err := t.saveTeacherHomepageTeachers(in); err != nil {
		return err
	}
	if err := t.bumpHomepageReview(in.SchoolID); err != nil {
		return err
	}
	if _, err := t.applyHomepageMembership(in.SchoolID, at); err != nil {
		return err
	}
	// Previous Admin clients continue to consume the original automatic
	// classification. The review endpoints show the current human decisions.
	legacy := c.TeacherUnmatchedSnapshot{
		SchoolID: in.SchoolID, Revision: in.Revision, ObservedAt: in.ObservedAt, Items: []c.UnmatchedTeacherSite{},
	}
	for _, site := range in.Sites {
		if !slices.Contains(c.TeacherUnmatchedReasons, site.Reason) {
			continue
		}
		legacy.Items = append(legacy.Items, c.UnmatchedTeacherSite{
			SiteID: site.SiteID, Name: site.Name, Unit: site.Unit, Reason: site.Reason, CandidateCount: site.CandidateCount,
		})
	}
	slices.SortFunc(legacy.Items, func(a, b c.UnmatchedTeacherSite) int {
		return cmp.Compare(a.SiteID, b.SiteID)
	})
	return t.ReplaceTeacherUnmatched(legacy)
}

func (t *transaction) saveTeacherHomepageSites(in c.TeacherSiteSnapshot, bodyImageBudget int64) error {
	if err := t.Lock("teacher-homepage-body-images"); err != nil {
		return err
	}
	matches := make(map[string]c.TeacherSiteMatch, len(in.Matches))
	for _, match := range in.Matches {
		matches[match.URL] = match
	}

	// Profiles and photos are transmitted separately. Limit each profile batch
	// by encoded bytes so a sequence of large public profiles cannot fill RAM.
	const maxBatchBytes = 4 << 20
	batch := make([]json.RawMessage, 0, 500)
	batchBytes := 0
	flush := func() error {
		raw, err := json.Marshal(batch)
		if err != nil {
			return c.ErrInput
		}
		if err := t.upsertHomepageSites(in.SchoolID, in.Revision, string(raw)); err != nil {
			return err
		}
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	for _, site := range in.Sites {
		match := matches[site.URL]
		row := homepageSiteRow{
			SiteID: site.SiteID, Name: site.Name, Unit: site.Unit, URL: site.URL, Reason: site.Reason,
			CandidateIDs: site.CandidateTeacherIDs, Count: site.CandidateCount, Profile: site.Profile,
			AutoTeacher: match.TeacherID, AutoName: match.Name, AutoDept: match.DepartmentID,
		}
		row.BodyMedia = site.BodyMedia
		if row.BodyMedia == nil {
			row.BodyMedia = []c.TeacherHomepageBodyMedia{}
		}
		if site.Photo != nil {
			row.PhotoID = site.Photo.ID
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return c.ErrInput
		}
		if len(batch) == cap(batch) || batchBytes+len(encoded) > maxBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, encoded)
		batchBytes += len(encoded)
	}
	if len(batch) > 0 {
		if err := flush(); err != nil {
			return err
		}
	}
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_sites WHERE school_id=$1 AND revision<>$2`, in.SchoolID, in.Revision); err != nil {
		return safe(err)
	}
	if err := t.replaceTeacherHomepageBodyMedia(in, bodyImageBudget); err != nil {
		return err
	}
	return t.saveTeacherHomepagePhotos(in)
}

func (t *transaction) upsertHomepageSites(school string, revision int64, raw string) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_sites(
 school_id,site_id,revision,name,unit,url,reason,candidate_ids,candidate_count,profile,body_media,photo_id,auto_teacher_id,auto_name,auto_department_id)
 SELECT $1,r.site_id,$2,r.name,r.unit,r.url,r.reason,COALESCE(r.candidate_ids,ARRAY[]::text[]),r.candidate_count,
 r.profile,r.body_media,r.photo_id,r.auto_teacher_id,r.auto_name,r.auto_department_id
 FROM jsonb_to_recordset($3::jsonb) AS r(site_id text,name text,unit text,url text,reason text,
 candidate_ids text[],candidate_count bigint,profile jsonb,body_media jsonb,photo_id text,auto_teacher_id text,auto_name text,auto_department_id text)
 ON CONFLICT(school_id,site_id) DO UPDATE SET revision=excluded.revision,name=excluded.name,unit=excluded.unit,
 url=excluded.url,reason=excluded.reason,candidate_ids=excluded.candidate_ids,candidate_count=excluded.candidate_count,profile=excluded.profile,body_media=excluded.body_media,
 photo_id=excluded.photo_id,auto_teacher_id=excluded.auto_teacher_id,auto_name=excluded.auto_name,auto_department_id=excluded.auto_department_id`,
		school, revision, raw)
	return safe(err)
}

func (t *transaction) saveTeacherHomepageTeachers(in c.TeacherSiteSnapshot) error {
	if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_teachers WHERE school_id=$1`, in.SchoolID); err != nil {
		return safe(err)
	}
	type row struct {
		TeacherID      string   `json:"teacher_id"`
		Reason         string   `json:"reason"`
		CandidateIDs   []string `json:"candidate_ids"`
		CandidateCount int64    `json:"candidate_count"`
	}
	for page := range slices.Chunk(in.Teachers, 500) {
		batch := make([]row, 0, len(page))
		for _, teacher := range page {
			batch = append(batch, row{teacher.TeacherID, teacher.Reason, teacher.CandidateSiteIDs, teacher.CandidateCount})
		}
		raw, err := json.Marshal(batch)
		if err != nil {
			return c.ErrInput
		}
		result, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_teachers(school_id,teacher_id,reason,candidate_ids,candidate_count)
 SELECT $1,o.id,r.reason,COALESCE(r.candidate_ids,ARRAY[]::text[]),r.candidate_count
 FROM jsonb_to_recordset($2::jsonb) r(teacher_id text,reason text,candidate_ids text[],candidate_count bigint)
 JOIN catalog.objects o ON o.id=r.teacher_id AND o.school_id=$1 AND o.kind='teacher'
 WHERE o.id=catalog.canonical(o.id)`, in.SchoolID, string(raw))
		if err != nil {
			return safe(err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return safe(err)
		}
		if inserted != int64(len(batch)) {
			return c.ErrConflict
		}
	}
	return nil
}

func (t *transaction) saveTeacherHomepagePhotos(in c.TeacherSiteSnapshot) error {
	type storedPhoto struct {
		SiteID, ID, MediaType string
		Width, Height         int
	}
	reusable, err := list[storedPhoto](t, `SELECT jsonb_build_object('SiteID',p.site_id,'ID',p.photo_id,'MediaType',p.media_type,'Width',p.width,'Height',p.height)
 FROM catalog.teacher_homepage_photos p JOIN catalog.teacher_homepage_sites s
 ON s.school_id=p.school_id AND s.site_id=p.site_id AND s.photo_id=p.photo_id WHERE p.school_id=$1`, in.SchoolID)
	if err != nil {
		return err
	}
	existing := make(map[string]storedPhoto, len(reusable))
	for _, photo := range reusable {
		existing[photo.SiteID] = photo
	}
	for _, site := range in.Sites {
		if site.Photo == nil {
			continue
		}
		image := *site.Photo
		stored := existing[site.SiteID]
		if stored.ID == image.ID && stored.MediaType == image.MediaType && stored.Width == image.Width && stored.Height == image.Height {
			continue
		}
		if len(image.Bytes) == 0 {
			if in.ReadPhoto == nil {
				return c.ErrInput
			}
			image.Bytes, err = in.ReadPhoto(t.ctx, image.ID)
			if err != nil {
				if canceled := t.ctx.Err(); canceled != nil {
					return canceled
				}
				return c.ErrInput
			}
		}
		if err := c.ValidateTeacherHomepageImage(image); err != nil {
			return err
		}
		key := c.TeacherImageKey{Kind: c.SitePortrait, School: in.SchoolID, OwnerID: site.SiteID, Digest: image.ID}
		if err := t.imageStore().Write(t.ctx, key, image.Bytes, func(inline []byte) error {
			_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_photos(school_id,site_id,photo_id,media_type,width,height,data)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(school_id,site_id,photo_id) DO UPDATE SET media_type=excluded.media_type,width=excluded.width,height=excluded.height,data=excluded.data`,
				in.SchoolID, site.SiteID, image.ID, image.MediaType, image.Width, image.Height, inline)
			return safe(err)
		}); err != nil {
			return err
		}
	}
	_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_photos p USING catalog.teacher_homepage_sites s
 WHERE p.school_id=$1 AND s.school_id=p.school_id AND s.site_id=p.site_id AND s.photo_id<>p.photo_id`, in.SchoolID)
	return safe(err)
}

func (t *transaction) bumpHomepageReview(school string) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_review_state(school_id,revision) VALUES($1,1)
 ON CONFLICT(school_id) DO UPDATE SET revision=teacher_homepage_review_state.revision+1`, school)
	return safe(err)
}

// Manual decisions reserve a site and, for a binding, a catalog teacher. A
// reservation survives a missing website; only a currently usable site may
// supply the official marker. Automatic evidence remains available for revoke.
func (t *transaction) applyHomepageMembership(school string, at time.Time) ([]string, error) {
	return list[string](t, `WITH links AS MATERIALIZED (
 SELECT d.teacher_id,s.site_id,s.url,'' AS name,'' AS department_id,true AS manual
 FROM catalog.teacher_homepage_decisions d JOIN catalog.teacher_homepage_sites s USING(school_id,site_id)
 WHERE d.school_id=$1 AND d.action='bind' AND s.reason<>'site_unavailable'
 UNION ALL
 SELECT s.auto_teacher_id,s.site_id,s.url,s.auto_name,s.auto_department_id,false
 FROM catalog.teacher_homepage_sites s
 WHERE s.school_id=$1 AND s.reason<>'site_unavailable' AND s.auto_teacher_id<>''
 AND NOT EXISTS(SELECT 1 FROM catalog.teacher_homepage_decisions d WHERE d.school_id=$1 AND
 ((d.site_id=s.site_id AND d.action IN ('bind','reject')) OR (d.teacher_id=s.auto_teacher_id AND d.action='bind')))
 ), cleared AS (
 UPDATE catalog.teacher_directory td SET official=false,matched_name='',matched_department_id='',homepage_url='',
 homepage_site_id='',homepage_manual=false,official_updated_at=$2,updated_at=$2
 WHERE td.school_id=$1 AND td.official AND NOT EXISTS(SELECT 1 FROM links l WHERE l.teacher_id=td.teacher_id)
 RETURNING teacher_id
 ), activated AS (
 INSERT INTO catalog.teacher_directory AS td(teacher_id,school_id,official,matched_name,matched_department_id,homepage_url,homepage_site_id,
 homepage_manual,official_updated_at,updated_at)
 SELECT teacher_id,$1,true,name,department_id,url,site_id,manual,$2,$2 FROM links
 ON CONFLICT(teacher_id) DO UPDATE SET official=true,matched_name=excluded.matched_name,matched_department_id=excluded.matched_department_id,
 homepage_url=excluded.homepage_url,homepage_site_id=excluded.homepage_site_id,homepage_manual=excluded.homepage_manual,
 official_updated_at=excluded.official_updated_at,updated_at=excluded.updated_at
 WHERE NOT td.official OR (td.matched_name,td.matched_department_id,td.homepage_url,td.homepage_site_id,td.homepage_manual)
 IS DISTINCT FROM (excluded.matched_name,excluded.matched_department_id,excluded.homepage_url,excluded.homepage_site_id,excluded.homepage_manual)
 RETURNING teacher_id
 )
 SELECT to_jsonb(teacher_id) FROM cleared UNION ALL SELECT to_jsonb(teacher_id) FROM activated`, school, at)
}
