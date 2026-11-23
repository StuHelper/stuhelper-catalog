package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func officialTeacherCondition(directory, object string) string {
	return directory + `.official AND (` + directory + `.homepage_manual OR (` +
		directory + `.matched_name=` + object + `.name AND ` + directory + `.matched_department_id=` + object + `.department_id))`
}

func homepageTeacherEvidence(directory, object string) string {
	return directory + `.verified_homepage OR (` + officialTeacherCondition(directory, object) + `)`
}

// Automatic matches retain their source identity guard; a manual match uses
// the catalog identity selected by the school administrator.
func markedTeacherSQL(alias string) string {
	return `EXISTS(SELECT 1 FROM catalog.teacher_directory td WHERE td.teacher_id=` + alias + `.id
 AND td.school_id=` + alias + `.school_id
 AND (td.teaching OR ` + homepageTeacherEvidence("td", alias) + `))`
}

func (t *transaction) teacherVisibility(alias string) string {
	if !t.requireTeacherDirectory {
		return ""
	}
	return " AND (" + alias + ".kind<>'teacher' OR " + markedTeacherSQL(alias) + ")"
}

var _ c.TeacherHomepagePublicationTransaction = (*transaction)(nil)

func (t *transaction) GrantVerifiedTeacherHomepage(school, teacherID string, at time.Time) error {
	if err := t.requireActiveHomepageTeacher(school, teacherID); err != nil {
		return err
	}
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_directory AS td
 (teacher_id,school_id,verified_homepage,verified_homepage_at,updated_at)
 VALUES($1,$2,true,$3,$3)
 ON CONFLICT(teacher_id) DO UPDATE SET verified_homepage=true,
 verified_homepage_at=excluded.verified_homepage_at,updated_at=excluded.updated_at
 WHERE td.school_id=excluded.school_id AND NOT td.verified_homepage`, teacherID, school, at)
	if err != nil {
		return safe(err)
	}
	written, err := result.RowsAffected()
	if err != nil || written == 0 {
		return safe(err)
	}
	changed := []string{teacherID}
	if err := t.syncTeacherDiscoveryAndActivity(school, changed, at); err != nil {
		return err
	}
	// Publish through the projection worker. A school can continue reviewing
	// requests while an existing generation is being built.
	return t.markHomepageProjectionPending(school, changed)
}

const taughtTeachersSQL = `SELECT DISTINCT catalog.canonical(a.teacher_id) AS id
 FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=catalog.canonical(a.class_id) AND cl.kind='class' AND cl.school_id=a.school_id
 JOIN catalog.objects tc ON tc.id=catalog.canonical(cl.term_course_id) AND tc.kind='term_course' AND tc.school_id=a.school_id
 JOIN catalog.objects co ON co.id=catalog.canonical(tc.course_id) AND co.kind='course' AND co.school_id=a.school_id
 WHERE a.kind='assignment' AND a.school_id=$1`

func teachingEvidenceSQL(alias string) string {
	return `EXISTS(SELECT 1 FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=catalog.canonical(a.class_id) AND cl.kind='class' AND cl.school_id=a.school_id
 JOIN catalog.objects tc ON tc.id=catalog.canonical(cl.term_course_id) AND tc.kind='term_course' AND tc.school_id=a.school_id
 JOIN catalog.objects co ON co.id=catalog.canonical(tc.course_id) AND co.kind='course' AND co.school_id=a.school_id
 WHERE a.kind='assignment' AND a.school_id=` + alias + `.school_id
 AND a.teacher_id IN (SELECT ` + alias + `.id UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=` + alias + `.id))`
}

// Import pages update only affected teacher keys once, in the business
// transaction. This avoids both per-assignment round trips and full-table scans
// after each 500-object page. The final full-source refresh also repairs a
// preceding installation that had no markers yet.
func (t *transaction) flushTeachingDirectory() error {
	if len(t.pendingTeachers) == 0 {
		return nil
	}
	ids := make([]string, 0, len(t.pendingTeachers))
	for id := range t.pendingTeachers {
		ids = append(ids, id)
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_directory(teacher_id,school_id,teaching,teaching_updated_at,updated_at)
 SELECT o.id,o.school_id,`+teachingEvidenceSQL("o")+`,$2,$2 FROM catalog.objects o
 WHERE o.kind='teacher' AND o.id IN(SELECT catalog.canonical(id) FROM unnest($1::text[]) ids(id))
 ON CONFLICT(teacher_id) DO UPDATE SET teaching=excluded.teaching,teaching_updated_at=excluded.teaching_updated_at,updated_at=excluded.updated_at
 WHERE catalog.teacher_directory.teaching IS DISTINCT FROM excluded.teaching`, ids, t.teachingTime)
	if err != nil {
		return safe(err)
	}
	deferred := c.MaintenanceRebuildDeferred(t.ctx)
	if (t.repoTeacherDirectory || deferred) && len(t.pendingTeacherSchools) > 0 {
		schools := make([]string, 0, len(t.pendingTeacherSchools))
		for school := range t.pendingTeacherSchools {
			schools = append(schools, school)
		}
		slices.Sort(schools)
		for _, school := range schools {
			if t.repoTeacherDirectory {
				if err := t.syncTeacherDiscoveryAndActivity(school, ids, t.teachingTime); err != nil {
					return err
				}
			}
			if deferred {
				if err := t.markDeferredVerifiedHomepageChanges(school, ids); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// An asynchronous directory maintenance write can deactivate or merge an
// approved teacher before the next generation sees that change.
func (t *transaction) markDeferredVerifiedHomepageChanges(school string, ids []string) error {
	changed, err := list[string](t, `SELECT to_jsonb(teacher_id) FROM catalog.teacher_directory
 WHERE school_id=$1 AND verified_homepage AND teacher_id=ANY($2::text[])`, school, ids)
	if err != nil || len(changed) == 0 {
		return err
	}
	return t.markHomepageProjectionPending(school, changed)
}

func (t *transaction) TeacherCandidates(school, after string, limit int) ([]c.TeacherCandidate, error) {
	return list[c.TeacherCandidate](t, `SELECT jsonb_build_object('id',o.id,'name',o.name,'department_id',o.department_id,
 'unit',COALESCE(d.name,''),'title',COALESCE(o.data->'object'->>'biography',''),
 'teaching',CASE WHEN EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)
 THEN EXISTS(SELECT 1 FROM catalog.projection_heads h JOIN catalog.projection_objects p ON p.version=h.version AND p.id=o.id AND p.teaching WHERE h.school_id=$1)
 ELSE `+teachingEvidenceSQL("o")+` END)
 FROM catalog.objects o LEFT JOIN catalog.objects d ON d.id=o.department_id AND d.kind='department' AND d.school_id=o.school_id
 WHERE o.school_id=$1 AND o.kind='teacher' AND o.id>$2 AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)
 ORDER BY o.id LIMIT $3`, school, after, limit)
}

func (t *transaction) directoryCounts(school string) (c.TeacherDirectoryResult, error) {
	var active bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads WHERE school_id=$1)`, school).Scan(&active); err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
	}
	if active {
		return t.projectedMarks(school)
	}
	var out c.TeacherDirectoryResult
	official := officialTeacherCondition("td", "o")
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FILTER(WHERE td.teaching),
 count(*) FILTER(WHERE `+official+`),
 count(*) FILTER(WHERE td.teaching AND `+official+`), count(*)
 FROM catalog.objects o JOIN catalog.teacher_directory td ON td.teacher_id=o.id
 WHERE o.school_id=$1 AND o.kind='teacher' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND `+markedTeacherSQL("o"), school).Scan(&out.Teaching, &out.Official, &out.Both, &out.Eligible)
	return out, safe(err)
}

func (t *transaction) TeacherHomepageCounts(school string) (c.TeacherHomepageCounts, error) {
	var out c.TeacherHomepageCounts
	err := t.tx.QueryRowContext(t.ctx, `WITH evidence AS (
 SELECT COALESCE(CASE WHEN h.version IS NULL THEN td.teaching ELSE p.teaching END,false) AS teaching,
 COALESCE((`+officialTeacherCondition("td", "o")+`),false) AS official,
 COALESCE(td.verified_homepage,false) AS verified_homepage
 FROM catalog.objects o
 LEFT JOIN catalog.teacher_directory td ON td.teacher_id=o.id AND td.school_id=o.school_id
 LEFT JOIN catalog.projection_heads h ON h.school_id=o.school_id
 LEFT JOIN catalog.projection_objects p ON p.version=h.version AND p.id=o.id
 WHERE o.school_id=$1 AND o.kind='teacher' AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)
 )
 SELECT count(*) FILTER(WHERE teaching),count(*) FILTER(WHERE official),count(*) FILTER(WHERE teaching AND official),
 count(*) FILTER(WHERE teaching OR official OR verified_homepage),count(*) FILTER(WHERE verified_homepage)
 FROM evidence`, school).Scan(&out.Teaching, &out.Official, &out.Both, &out.Eligible, &out.VerifiedHomepage)
	return out, safe(err)
}

func (t *transaction) RefreshTeachingDirectory(school string, at time.Time) (c.TeacherDirectoryResult, bool, error) {
	if !at.IsZero() && at.After(t.mutationTime) {
		t.mutationTime = at
	}
	if err := t.touchProjection(school); err != nil {
		return c.TeacherDirectoryResult{}, false, err
	}
	result, err := t.tx.ExecContext(t.ctx, `WITH taught AS (`+taughtTeachersSQL+`)
 INSERT INTO catalog.teacher_directory(teacher_id,school_id,teaching,teaching_updated_at,updated_at)
 SELECT o.id,o.school_id,EXISTS(SELECT 1 FROM taught WHERE taught.id=o.id),$2,$2
 FROM catalog.objects o WHERE o.school_id=$1 AND o.kind='teacher'
 ON CONFLICT(teacher_id) DO UPDATE SET teaching=excluded.teaching,teaching_updated_at=excluded.teaching_updated_at,updated_at=excluded.updated_at
 WHERE catalog.teacher_directory.teaching IS DISTINCT FROM excluded.teaching`, school, at)
	if err != nil {
		return c.TeacherDirectoryResult{}, false, safe(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return c.TeacherDirectoryResult{}, false, safe(err)
	}
	if err := t.syncTeacherDiscoveryAndActivity(school, nil, at); err != nil {
		return c.TeacherDirectoryResult{}, false, err
	}
	counts, err := t.directoryCounts(school)
	return counts, changed > 0, err
}

type teacherSourceState struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

func (t *transaction) teacherCandidateFingerprint(school string) (string, int, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT o.id,o.name,o.department_id,COALESCE(d.name,'') FROM catalog.objects o
 LEFT JOIN catalog.objects d ON d.id=o.department_id AND d.kind='department' AND d.school_id=o.school_id
 WHERE o.school_id=$1 AND o.kind='teacher' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id) ORDER BY o.id LIMIT 100001`, school)
	if err != nil {
		return "", 0, safe(err)
	}
	defer rows.Close()
	candidates := []c.TeacherCandidate{}
	for rows.Next() {
		var candidate c.TeacherCandidate
		if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.DepartmentID, &candidate.Unit); err != nil {
			return "", 0, safe(err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return "", 0, safe(err)
	}
	if len(candidates) > 100000 {
		return "", 0, c.ErrUnavailable
	}
	return c.TeacherCandidateFingerprint(candidates), len(candidates), nil
}

func (t *transaction) ReplaceTeacherSites(in c.TeacherSiteSnapshot, actor string, at time.Time) (c.TeacherDirectoryResult, error) {
	digest, err := teacherSiteSnapshotDigest(in)
	if err != nil {
		return c.TeacherDirectoryResult{}, c.ErrInput
	}
	old, err := read[teacherSourceState](t, `SELECT jsonb_build_object('revision',revision,'digest',digest) FROM catalog.teacher_site_sources WHERE school_id=$1`, in.SchoolID)
	if err == nil {
		if in.Revision < old.Revision || in.Revision == old.Revision && digest != old.Digest {
			return c.TeacherDirectoryResult{}, c.ErrConflict
		}
		if in.Revision == old.Revision {
			return t.directoryCounts(in.SchoolID)
		}
	} else if !errors.Is(err, c.ErrNotFound) {
		return c.TeacherDirectoryResult{}, err
	}
	var newerDetail bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT FROM catalog.teacher_homepage_sites WHERE school_id=$1 AND partial_digest<>'' AND revision>=$2)`, in.SchoolID, in.Revision).Scan(&newerDetail); err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
	}
	if newerDetail {
		return c.TeacherDirectoryResult{}, c.ErrConflict
	}
	if at.After(t.mutationTime) {
		t.mutationTime = at
	}
	if err := t.touchProjection(in.SchoolID); err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	currentFingerprint, candidateCount, err := t.teacherCandidateFingerprint(in.SchoolID)
	if err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	if currentFingerprint != in.CandidateFingerprint || in.Sites != nil && candidateCount != len(in.Teachers) {
		return c.TeacherDirectoryResult{}, c.ErrConflict
	}
	matches, err := json.Marshal(in.Matches)
	if err != nil {
		return c.TeacherDirectoryResult{}, c.ErrInput
	}
	var valid int
	err = t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM jsonb_to_recordset($1::jsonb) m(teacher_id text,name text,department_id text,url text)
 JOIN catalog.objects o ON o.id=m.teacher_id AND o.kind='teacher' AND o.school_id=$2 AND o.name=m.name AND o.department_id=m.department_id
 WHERE NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)`, string(matches), in.SchoolID).Scan(&valid)
	if err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
	}
	if valid != len(in.Matches) {
		return c.TeacherDirectoryResult{}, c.ErrConflict
	}
	if in.Sites != nil {
		err = t.replaceTeacherHomepageSnapshot(in, at)
	} else {
		err = t.replaceLegacyTeacherSiteMatches(in.SchoolID, string(matches), at)
	}
	if err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_site_sources(school_id,source_id,revision,digest,observed_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(school_id) DO UPDATE SET source_id=excluded.source_id,revision=excluded.revision,digest=excluded.digest,observed_at=excluded.observed_at,updated_at=excluded.updated_at`, in.SchoolID, in.SourceID, in.Revision, digest, in.ObservedAt, at)
	if err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
	}
	if err := t.Event(c.Event{ID: fmt.Sprintf("teacher-sites:%s:%d", in.SchoolID, in.Revision), Subject: "school/" + in.SchoolID,
		Type: "catalog.teacher-sites.refreshed.v1", SchoolID: in.SchoolID, Actor: actor, Revision: in.Revision, Result: "success", Time: at}); err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	if err := t.publishTeacherSiteMarks(in.SchoolID, at); err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	return t.directoryCounts(in.SchoolID)
}

func teacherSiteSnapshotDigest(in c.TeacherSiteSnapshot) (string, error) {
	hash := sha256.New()
	if in.Sites == nil {
		// Preserve the digest of legacy snapshots to support existing replays.
		raw, err := json.Marshal(in)
		if err != nil {
			return "", err
		}
		hash.Write(raw)
	} else {
		sites, teachers := in.Sites, in.Teachers
		in.Sites, in.Teachers = nil, nil
		encoder := json.NewEncoder(hash)
		if err := encoder.Encode(in); err != nil {
			return "", err
		}
		if err := encoder.Encode([2]int{len(sites), len(teachers)}); err != nil {
			return "", err
		}
		for _, site := range sites {
			if err := encoder.Encode(site); err != nil {
				return "", err
			}
		}
		for _, teacher := range teachers {
			if err := encoder.Encode(teacher); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (t *transaction) replaceLegacyTeacherSiteMatches(school, matches string, at time.Time) error {
	var v2 bool
	if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.teacher_homepage_review_state WHERE school_id=$1)`, school).Scan(&v2); err != nil {
		return safe(err)
	}
	if v2 {
		return c.ErrConflict
	}
	// Clear the preceding complete set and set this set within the same transaction.
	_, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_directory SET official=false,official_updated_at=$2,updated_at=$2,
 matched_name='',matched_department_id='',homepage_url='' WHERE school_id=$1 AND official`, school, at)
	if err != nil {
		return safe(err)
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_directory(teacher_id,school_id,official,matched_name,matched_department_id,homepage_url,official_updated_at,updated_at)
 SELECT teacher_id,$2,true,name,department_id,url,$3,$3 FROM jsonb_to_recordset($1::jsonb) m(teacher_id text,name text,department_id text,url text)
 ON CONFLICT(teacher_id) DO UPDATE SET official=true,matched_name=excluded.matched_name,matched_department_id=excluded.matched_department_id,
 homepage_url=excluded.homepage_url,official_updated_at=excluded.official_updated_at,updated_at=excluded.updated_at`, matches, school, at)
	return safe(err)
}

func (t *transaction) publishTeacherSiteMarks(school string, at time.Time) error {
	if err := t.syncTeacherDiscoveryAndActivity(school, nil, at); err != nil {
		return err
	}
	report, err := t.RebuildPublishedDirectory(school, at)
	if err != nil || report == nil {
		return err
	}
	// This complete generation now reflects every decision under the school
	// lock. Its teacher snapshot replaces the temporary read overlay.
	_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.projection_pending SET homepage_revoked_ids='{}',verified_teachers_unavailable_ids='{}'
 WHERE school_id=$1 AND cardinality(homepage_revoked_ids)+cardinality(verified_teachers_unavailable_ids)>0`, school)
	return safe(err)
}
