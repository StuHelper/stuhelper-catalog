package postgres

import c "github.com/StuHelper/stuhelper-catalog"

const currentReviewTeacher = `o.kind='teacher' AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 AND NOT EXISTS (SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)`

// Reuse the directory's effective match predicate. A snapshot candidate alone
// cannot make a teacher matched after a rename, deactivation, or merge.
func teacherReviewMatches() string {
	return `SELECT td.homepage_site_id AS site_id, td.teacher_id
 FROM catalog.teacher_directory td JOIN catalog.objects o ON o.id=td.teacher_id AND o.school_id=td.school_id
 WHERE td.school_id=$1 AND td.homepage_site_id<>'' AND ` + currentReviewTeacher + `
 AND ` + officialTeacherCondition("td", "o")
}

type teacherReviewPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Revision int64 `json:"revision"`
}

func (t *transaction) ReadTeacherSiteReviews(q c.TeacherReviewQuery) (c.TeacherSiteReviewPage, error) {
	// A removed site is still reviewable while an effective decision exists.
	// Only the selected page fetches profile text and expands candidate IDs.
	result, err := read[teacherReviewPage[c.TeacherSiteReview]](t, `
 WITH state AS (SELECT revision FROM catalog.teacher_homepage_review_state WHERE school_id=$1),
 matches AS (`+teacherReviewMatches()+`),
 site_rows AS (
  SELECT COALESCE(s.site_id,d.site_id) AS site_id, COALESCE(s.name,d.name) AS name, COALESCE(s.unit,d.unit) AS unit,
         COALESCE(s.reason,'site_unavailable') AS reason,
         COALESCE(s.reason<>'site_unavailable',false) AS present,
         COALESCE(s.candidate_ids,'{}'::text[]) AS candidate_ids, COALESCE(s.candidate_count,0) AS candidate_count,
         s.auto_teacher_id, COALESCE(d.revision,0) AS decision_revision,
         CASE WHEN d.action IN ('bind','reject') THEN d.action ELSE '' END AS action, d.teacher_id AS decision_teacher_id
  FROM (SELECT * FROM catalog.teacher_homepage_sites WHERE school_id=$1) s
  FULL JOIN (SELECT * FROM catalog.teacher_homepage_decisions WHERE school_id=$1) d USING(site_id)
  WHERE EXISTS(SELECT 1 FROM state) AND (s.site_id IS NOT NULL OR d.action IN ('bind','reject'))
 ), filtered AS (
  SELECT site_rows.*, m.teacher_id AS matched_teacher_id FROM site_rows LEFT JOIN matches m USING(site_id)
  WHERE ($2='all' OR ($2='decided' AND action<>'') OR ($2='pending' AND action='' AND m.teacher_id IS NULL))
    AND ($3='' OR strpos(site_rows.site_id,lower($3))>0 OR strpos(lower(name),lower($3))>0 OR strpos(lower(unit),lower($3))>0)
 ), page AS (
  SELECT * FROM filtered WHERE site_id>$4 ORDER BY site_id LIMIT $5
 ), items AS (
  SELECT p.site_id, jsonb_build_object(
   'site_id',p.site_id,'name',p.name,'unit',p.unit,'present',p.present,
   'reason',CASE
    WHEN NOT p.present THEN 'site_unavailable'
    WHEN p.action='bind' AND p.matched_teacher_id IS NULL THEN 'snapshot_changed'
    WHEN p.matched_teacher_id IS NOT NULL OR p.action<>'' THEN ''
    WHEN p.reason<>'' THEN p.reason
    WHEN reserved.site_id IS NOT NULL THEN 'manual_conflict'
    ELSE 'snapshot_changed' END,
   'matched_teacher_id',COALESCE(p.matched_teacher_id,''),
   'decision',CASE WHEN p.action<>'' THEN jsonb_build_object('action',p.action,'teacher_id',p.decision_teacher_id) END,
   'decision_revision',p.decision_revision,'candidate_count',p.candidate_count,
   'candidates',candidates.items,'biography_excerpt',left(s.profile->>'biography',320),
   'discipline',s.profile->>'discipline'
  ) AS item
  FROM page p
  LEFT JOIN catalog.teacher_homepage_sites s ON s.school_id=$1 AND s.site_id=p.site_id
  LEFT JOIN catalog.teacher_homepage_decisions reserved
    ON reserved.school_id=$1 AND reserved.action='bind' AND reserved.teacher_id=p.auto_teacher_id AND reserved.site_id<>p.site_id
  CROSS JOIN LATERAL (
   SELECT COALESCE(jsonb_agg(jsonb_build_object('teacher_id',x.id,'name',x.name,'department_id',x.department_id,
          'department',x.department,'title',x.title) ORDER BY x.id),'[]'::jsonb) AS items
   FROM (
    SELECT o.id,o.name,o.department_id,d.name AS department,left(o.data->'object'->>'biography',512) AS title
    FROM catalog.objects o LEFT JOIN catalog.objects d ON d.id=o.department_id AND d.kind='department' AND d.school_id=$1
    WHERE o.id=ANY(p.candidate_ids) AND o.school_id=$1 AND `+currentReviewTeacher+`
    ORDER BY o.id LIMIT 100
   ) x
  ) candidates
 )
 SELECT jsonb_build_object('revision',COALESCE((SELECT revision FROM state),0), 'total',(SELECT count(*) FROM filtered),
   'items',COALESCE((SELECT jsonb_agg(item ORDER BY site_id) FROM items),'[]'::jsonb))`,
		q.SchoolID, q.Status, q.Query, q.After, q.Limit)
	return c.TeacherSiteReviewPage{Items: result.Items, Total: result.Total, Revision: result.Revision}, err
}

func (t *transaction) ReadTeacherReviews(q c.TeacherReviewQuery) (c.TeacherReviewPage, error) {
	result, err := read[teacherReviewPage[c.TeacherReview]](t, `
 WITH state AS (SELECT revision FROM catalog.teacher_homepage_review_state WHERE school_id=$1),
 matches AS (`+teacherReviewMatches()+`),
 filtered AS (
  SELECT o.id, o.name, o.department_id, d.name AS department, m.site_id AS matched_site_id
  FROM catalog.objects o
  LEFT JOIN catalog.objects d ON d.id=o.department_id AND d.kind='department' AND d.school_id=$1
  LEFT JOIN matches m ON m.teacher_id=o.id
  WHERE EXISTS(SELECT 1 FROM state) AND o.school_id=$1 AND `+currentReviewTeacher+`
    AND ($2='all' OR m.site_id IS NULL)
    AND ($3='' OR strpos(lower(o.id),lower($3))>0 OR strpos(lower(o.name),lower($3))>0 OR strpos(lower(d.name),lower($3))>0)
 ), page AS (
  SELECT * FROM filtered WHERE id>$4 ORDER BY id LIMIT $5
 ), items AS (
  SELECT p.id, jsonb_build_object('teacher_id',p.id,'name',p.name,'department_id',p.department_id,
   'department',p.department,'title',left(o.data->'object'->>'biography',512),
   'matched_site_id',COALESCE(p.matched_site_id,''),
   'reason',CASE
    WHEN p.matched_site_id IS NOT NULL THEN ''
    WHEN reserved.site_id IS NOT NULL AND (bound.site_id IS NULL OR bound.reason='site_unavailable') THEN 'site_unavailable'
    WHEN tr.teacher_id IS NULL THEN 'awaiting_snapshot'
    WHEN tr.reason<>'' THEN tr.reason
    WHEN EXISTS (
     SELECT 1 FROM catalog.teacher_homepage_sites s
     JOIN catalog.teacher_homepage_decisions d ON d.school_id=s.school_id AND d.site_id=s.site_id AND d.action IN ('bind','reject')
     WHERE s.school_id=$1 AND s.site_id=ANY(tr.candidate_ids) AND s.auto_teacher_id=p.id
    ) THEN 'manual_conflict'
    ELSE 'snapshot_changed' END,
   'candidate_count',COALESCE(tr.candidate_count,0),'candidates',candidates.items
  ) AS item
  FROM page p JOIN catalog.objects o ON o.id=p.id AND o.school_id=$1
  LEFT JOIN catalog.teacher_homepage_teachers tr ON tr.teacher_id=p.id AND tr.school_id=$1
  LEFT JOIN catalog.teacher_homepage_decisions reserved
    ON reserved.school_id=$1 AND reserved.action='bind' AND reserved.teacher_id=p.id
  LEFT JOIN catalog.teacher_homepage_sites bound ON bound.school_id=$1 AND bound.site_id=reserved.site_id
  CROSS JOIN LATERAL (
   SELECT COALESCE(jsonb_agg(jsonb_build_object('site_id',x.site_id,'name',x.name,'unit',x.unit,
          'present',x.present) ORDER BY x.site_id),'[]'::jsonb) AS items
   FROM (
    SELECT s.site_id,s.name,s.unit,s.reason<>'site_unavailable' AS present
    FROM catalog.teacher_homepage_sites s WHERE s.school_id=$1 AND s.site_id=ANY(tr.candidate_ids)
    ORDER BY s.site_id LIMIT 100
   ) x
  ) candidates
 )
 SELECT jsonb_build_object('revision',COALESCE((SELECT revision FROM state),0), 'total',(SELECT count(*) FROM filtered),
   'items',COALESCE((SELECT jsonb_agg(item ORDER BY id) FROM items),'[]'::jsonb))`,
		q.SchoolID, q.Status, q.Query, q.After, q.Limit)
	return c.TeacherReviewPage{Items: result.Items, Total: result.Total, Revision: result.Revision}, err
}
