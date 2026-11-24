package postgres

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (t *transaction) HomepageDetailScope(school string) (c.HomepageDetailScope, error) {
	out := c.HomepageDetailScope{SchoolID: school, Format: "teacher-homepage-scope-v1"}
	if err := t.tx.QueryRowContext(t.ctx, `SELECT s.revision,r.revision FROM catalog.teacher_site_sources s
 JOIN catalog.teacher_homepage_review_state r USING(school_id) WHERE s.school_id=$1`, school).Scan(&out.SourceRevision, &out.ReviewRevision); err != nil {
		return out, safe(err)
	}
	rows, err := list[c.HomepageScopeSite](t, `WITH matches AS (`+teacherReviewMatches()+`)
 SELECT jsonb_build_object('site_id',s.site_id,'url',s.url,'name',s.name,'teacher_id',COALESCE(m.teacher_id,''))
 FROM catalog.teacher_homepage_sites s LEFT JOIN matches m USING(site_id)
 WHERE s.school_id=$1 ORDER BY s.site_id LIMIT 20001`, school)
	if err != nil {
		return out, err
	}
	if len(rows) == 0 || len(rows) > 20000 {
		return out, c.ErrConflict
	}
	out.Sites, out.Total = rows, len(rows)
	return out, nil
}

func (t *transaction) ApplyHomepageDetailPatch(in c.HomepageDetailPatch, actor string, at time.Time) error {
	body, err := json.Marshal(in)
	if err != nil {
		return c.ErrInput
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	var oldProfile c.TeacherHomepage
	var profile, base []byte
	var revision int64
	var oldHash string
	var matched bool
	err = t.tx.QueryRowContext(t.ctx, `WITH matches AS (`+teacherReviewMatches()+`)
 SELECT s.revision,s.profile,s.partial_base, s.partial_digest,
 (r.revision=$5 AND EXISTS(SELECT FROM matches WHERE site_id=s.site_id AND teacher_id=$6))
 FROM catalog.teacher_homepage_sites s JOIN catalog.teacher_site_sources src USING(school_id)
 JOIN catalog.teacher_homepage_review_state r USING(school_id)
 WHERE s.school_id=$1 AND s.site_id=$2 AND s.url=$3 AND src.revision=$4 FOR UPDATE OF s`,
		in.SchoolID, in.SiteID, in.URL, in.SourceRevision, in.ReviewRevision, in.TeacherID).Scan(&revision, &profile, &base, &oldHash, &matched)
	if errors.Is(err, sql.ErrNoRows) {
		return c.ErrConflict
	}
	if err != nil {
		return safe(err)
	}
	if revision == in.CheckedAt.UnixMicro() && oldHash == sum {
		return nil
	}
	if !matched || revision >= in.CheckedAt.UnixMicro() {
		return c.ErrConflict
	}
	if json.Unmarshal(profile, &oldProfile) != nil {
		return c.ErrUnavailable
	}
	if in.PageStatus == "not_found" {
		return t.withdrawPartialHomepage(in, sum, actor, at)
	}
	if base == nil {
		// Only the rich full-crawl lists are needed to merge later homepage lists.
		base, err = json.Marshal(c.TeacherHomepage{Fields: oldProfile.Fields, Sections: oldProfile.Sections})
		if err != nil {
			return c.ErrInput
		}
	}
	var original c.TeacherHomepage
	if json.Unmarshal(base, &original) != nil {
		return c.ErrUnavailable
	}
	// Full-crawl text remains the baseline for dynamic lists. A fresh homepage
	// observation replaces its previous contribution to each observed list.
	if _, ok := in.Set["fields"]; ok {
		oldProfile.Fields = original.Fields
	}
	if _, ok := in.Set["sections"]; ok {
		oldProfile.Sections = original.Sections
	}
	updated, err := c.MergeHomepageDetail(oldProfile, in.Set)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return c.ErrInput
	}
	observations := map[string]time.Time{}
	for key := range in.Set {
		observations[key] = in.CheckedAt
	}
	if in.Photo != nil {
		observations["photo"] = in.CheckedAt
	}
	dates, err := json.Marshal(observations)
	if err != nil {
		return c.ErrInput
	}
	_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_homepage_sites SET profile=$3::jsonb,partial_base=$4::jsonb,
 partial_digest=$5,revision=$6,partial_dates=partial_dates || $7::jsonb WHERE school_id=$1 AND site_id=$2`, in.SchoolID, in.SiteID, string(raw), string(base), sum, in.CheckedAt.UnixMicro(), string(dates))
	if err != nil {
		return safe(err)
	}
	if photo := in.Photo; photo != nil {
		if _, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_homepage_sites SET photo_id=$3 WHERE school_id=$1 AND site_id=$2`, in.SchoolID, in.SiteID, photo.ID); err != nil {
			return safe(err)
		}
		if err := t.writeHomepagePhoto(in.SchoolID, in.SiteID, *photo); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_photos WHERE school_id=$1 AND site_id=$2 AND photo_id<>$3`, in.SchoolID, in.SiteID, photo.ID); err != nil {
			return safe(err)
		}
	}
	return t.partialHomepageEvent(in, actor, at)
}

func (t *transaction) withdrawPartialHomepage(in c.HomepageDetailPatch, sum, actor string, at time.Time) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_homepage_sites SET reason='site_unavailable',profile='{}',photo_id='',body_media='[]',
 partial_base=NULL,partial_digest=$3,partial_dates='{}',revision=$4 WHERE school_id=$1 AND site_id=$2`, in.SchoolID, in.SiteID, sum, in.CheckedAt.UnixMicro())
	if err != nil {
		return safe(err)
	}
	if _, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_photos WHERE school_id=$1 AND site_id=$2`, in.SchoolID, in.SiteID); err != nil {
		return safe(err)
	}
	if _, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.teacher_homepage_body_refs WHERE school_id=$1 AND site_id=$2`, in.SchoolID, in.SiteID); err != nil {
		return safe(err)
	}
	changed, err := t.applyHomepageMembership(in.SchoolID, at)
	if err != nil {
		return err
	}
	if err = t.bumpHomepageReview(in.SchoolID); err != nil {
		return err
	}
	if len(changed) > 0 {
		if err = t.syncTeacherDiscoveryAndActivity(in.SchoolID, changed, at); err != nil {
			return err
		}
		if err = t.markHomepageProjectionPending(in.SchoolID, changed); err != nil {
			return err
		}
	}
	return t.partialHomepageEvent(in, actor, at)
}

func (t *transaction) partialHomepageEvent(in c.HomepageDetailPatch, actor string, at time.Time) error {
	return t.Event(c.Event{ID: fmt.Sprintf("teacher-detail:%s:%d", in.SiteID, in.CheckedAt.UnixMicro()), Subject: "school/" + in.SchoolID,
		Type: "catalog.teacher-homepage.partial.v1", SchoolID: in.SchoolID, Actor: actor, Time: at, Revision: in.CheckedAt.UnixMicro(), Result: "success"})
}
