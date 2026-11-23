package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (t *transaction) RestoreTeacherHomepageText(in c.TeacherSiteSnapshot, fileDigest string, checkedAt map[string]time.Time, actor string, at time.Time) error {
	inputDigest, err := teacherSiteSnapshotDigest(in)
	if err != nil {
		return c.ErrInput
	}
	observations, err := json.Marshal(checkedAt)
	if err != nil {
		return c.ErrInput
	}
	var restored, identical bool
	err = t.tx.QueryRowContext(t.ctx, `SELECT legacy_text_receipt IS NOT NULL,
 COALESCE(legacy_text_receipt->>'file_sha256'=$5 AND legacy_text_receipt->>'input_sha256'=$6 AND legacy_text_receipt->'checked_at'=$7::jsonb,false)
 FROM catalog.teacher_site_sources WHERE school_id=$1 AND source_id=$2 AND revision=$3 AND observed_at=$4 FOR UPDATE`,
		in.SchoolID, in.SourceID, in.Revision, in.ObservedAt, fileDigest, inputDigest, string(observations)).Scan(&restored, &identical)
	if errors.Is(err, sql.ErrNoRows) || restored && !identical {
		return c.ErrConflict
	}
	if err != nil || restored {
		return safe(err)
	}
	current, count, err := t.teacherCandidateFingerprint(in.SchoolID)
	if err != nil {
		return err
	}
	if current != in.CandidateFingerprint || count != len(in.Teachers) {
		return c.ErrConflict
	}

	var empty bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT NOT EXISTS(SELECT FROM catalog.teacher_homepage_sites WHERE school_id=$1)
 AND NOT EXISTS(SELECT FROM catalog.teacher_homepage_teachers WHERE school_id=$1)
 AND EXISTS(SELECT FROM catalog.teacher_unmatched_snapshots WHERE school_id=$1 AND revision=$2 AND observed_at=$3)`,
		in.SchoolID, in.Revision, in.ObservedAt).Scan(&empty); err != nil {
		return safe(err)
	}
	if !empty {
		return c.ErrConflict
	}

	if err := t.writeHomepageSiteRows(in); err != nil {
		return err
	}
	if err := t.verifyLegacyHomepageMembership(in); err != nil {
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

	_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_site_sources SET legacy_text_receipt=jsonb_build_object(
 'file_sha256',$2::text,'input_sha256',$3::text,'source_revision',revision,'observed_at',observed_at,
 'restored_at',$4::timestamptz,'execution_revision',$5::bigint,'checked_at',$6::jsonb) WHERE school_id=$1`, in.SchoolID, fileDigest, inputDigest, at, at.UnixMicro(), string(observations))
	if err != nil {
		return safe(err)
	}
	return t.Event(c.Event{ID: fmt.Sprintf("teacher-homepage-restore:%s:%d", in.SchoolID, in.Revision),
		Subject: "school/" + in.SchoolID, Type: "catalog.teacher-homepage.text-restored.v1", SchoolID: in.SchoolID,
		Actor: actor, Result: "success", Time: at, Revision: at.UnixMicro()})
}

// Compare both sets after staging the sites under the school lock. A mismatch
// rolls back every staged row and leaves pending reviews and decisions intact.
func (t *transaction) verifyLegacyHomepageMembership(in c.TeacherSiteSnapshot) error {
	matches, err := json.Marshal(in.Matches)
	if err != nil {
		return c.ErrInput
	}
	var same bool
	err = t.tx.QueryRowContext(t.ctx, `WITH expected AS (
 SELECT teacher_id,name,department_id,url FROM jsonb_to_recordset($2::jsonb) AS m(teacher_id text,name text,department_id text,url text)
 ), actual AS (
 SELECT teacher_id,matched_name AS name,matched_department_id AS department_id,homepage_url AS url
 FROM catalog.teacher_directory WHERE school_id=$1 AND official AND NOT homepage_manual
 ), pending AS (
 SELECT site_id,name,unit,reason,candidate_count FROM catalog.teacher_homepage_sites WHERE school_id=$1 AND reason<>''
 ), legacy AS (
 SELECT site_id,data->>'name' AS name,data->>'unit' AS unit,reason,(data->>'candidate_count')::bigint AS candidate_count
 FROM catalog.teacher_unmatched_sites WHERE school_id=$1
 )
 SELECT NOT EXISTS ((TABLE actual EXCEPT TABLE expected) UNION ALL (TABLE expected EXCEPT TABLE actual))
 AND NOT EXISTS ((TABLE legacy EXCEPT TABLE pending) UNION ALL (TABLE pending EXCEPT TABLE legacy))`,
		in.SchoolID, string(matches)).Scan(&same)
	if err != nil {
		return safe(err)
	}
	if !same {
		return c.ErrConflict
	}
	return nil
}
