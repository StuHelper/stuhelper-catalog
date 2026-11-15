package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

// SQL visibility is applied before pagination and aggregation. An official match
// is invalidated immediately if the matched catalog name or department changes.
func markedTeacherSQL(alias string) string {
	return `EXISTS(SELECT 1 FROM catalog.teacher_directory td WHERE td.teacher_id=` + alias + `.id
 AND (td.teaching OR (td.official AND td.matched_name=` + alias + `.name AND td.matched_department_id=` + alias + `.department_id)))`
}

func (t *transaction) teacherVisibility(alias string) string {
	if !t.requireTeacherDirectory {
		return ""
	}
	return " AND (" + alias + ".kind<>'teacher' OR " + markedTeacherSQL(alias) + ")"
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
	return safe(err)
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
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FILTER(WHERE td.teaching),
 count(*) FILTER(WHERE td.official AND td.matched_name=o.name AND td.matched_department_id=o.department_id),
 count(*) FILTER(WHERE td.teaching AND td.official AND td.matched_name=o.name AND td.matched_department_id=o.department_id), count(*)
 FROM catalog.objects o JOIN catalog.teacher_directory td ON td.teacher_id=o.id
 WHERE o.school_id=$1 AND o.kind='teacher' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false) AND `+markedTeacherSQL("o"), school).Scan(&out.Teaching, &out.Official, &out.Both, &out.Eligible)
	return out, safe(err)
}

func (t *transaction) RefreshTeachingDirectory(school string, at time.Time) (c.TeacherDirectoryResult, bool, error) {
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
	counts, err := t.directoryCounts(school)
	return counts, changed > 0, err
}

type teacherSourceState struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

func (t *transaction) teacherCandidateFingerprint(school string) (string, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT o.id,o.name,o.department_id,COALESCE(d.name,'') FROM catalog.objects o
 LEFT JOIN catalog.objects d ON d.id=o.department_id AND d.kind='department' AND d.school_id=o.school_id
 WHERE o.school_id=$1 AND o.kind='teacher' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id) ORDER BY o.id LIMIT 100001`, school)
	if err != nil {
		return "", safe(err)
	}
	defer rows.Close()
	candidates := []c.TeacherCandidate{}
	for rows.Next() {
		var candidate c.TeacherCandidate
		if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.DepartmentID, &candidate.Unit); err != nil {
			return "", safe(err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return "", safe(err)
	}
	if len(candidates) > 100000 {
		return "", c.ErrUnavailable
	}
	return c.TeacherCandidateFingerprint(candidates), nil
}

func (t *transaction) ReplaceTeacherSites(in c.TeacherSiteSnapshot, actor string, at time.Time) (c.TeacherDirectoryResult, error) {
	if err := t.touchProjection(in.SchoolID); err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return c.TeacherDirectoryResult{}, c.ErrInput
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
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
	currentFingerprint, err := t.teacherCandidateFingerprint(in.SchoolID)
	if err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	if currentFingerprint != in.CandidateFingerprint {
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
	// Clear the preceding complete set and set this set within the same transaction.
	_, err = t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_directory SET official=false,official_updated_at=$2,updated_at=$2,
 matched_name='',matched_department_id='',homepage_url='' WHERE school_id=$1 AND official`, in.SchoolID, at)
	if err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_directory(teacher_id,school_id,official,matched_name,matched_department_id,homepage_url,official_updated_at,updated_at)
 SELECT teacher_id,$2,true,name,department_id,url,$3,$3 FROM jsonb_to_recordset($1::jsonb) m(teacher_id text,name text,department_id text,url text)
 ON CONFLICT(teacher_id) DO UPDATE SET official=true,matched_name=excluded.matched_name,matched_department_id=excluded.matched_department_id,
 homepage_url=excluded.homepage_url,official_updated_at=excluded.official_updated_at,updated_at=excluded.updated_at`, string(matches), in.SchoolID, at)
	if err != nil {
		return c.TeacherDirectoryResult{}, safe(err)
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
	if err := t.rebuildActive(in.SchoolID); err != nil {
		return c.TeacherDirectoryResult{}, err
	}
	return t.directoryCounts(in.SchoolID)
}
