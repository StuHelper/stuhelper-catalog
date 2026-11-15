package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) HasTaught(teachers []string, course string) (bool, error) {
	var yes bool
	if t.projectionRead {
		if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads)`).Scan(&yes); err != nil {
			return false, safe(err)
		}
		if !yes {
			return false, c.ErrUnavailable
		}
		err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.projection_heads h
 CROSS JOIN unnest($1::text[]) requested(id)
 LEFT JOIN catalog.projection_aliases ta ON ta.version=h.version AND ta.alias_id=requested.id
 LEFT JOIN catalog.projection_aliases ca ON ca.version=h.version AND ca.alias_id=$2
 JOIN catalog.projection_teaching_history history ON history.version=h.version
 AND history.teacher_id=COALESCE(ta.canonical_id,requested.id) AND history.course_id=COALESCE(ca.canonical_id,$2))`, teachers, course).Scan(&yes)
		return yes, safe(err)
	}
	err := t.tx.QueryRowContext(t.ctx, `WITH resolved AS(SELECT DISTINCT catalog.canonical(id) AS id FROM unnest($1::text[]) input(id)), teacher_ids AS (
 SELECT id FROM resolved
 UNION SELECT r.alias_id FROM catalog.redirects r JOIN resolved ON resolved.id=r.canonical_id
 ), course_ids AS (SELECT catalog.canonical($2) AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=catalog.canonical($2))
 SELECT EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=catalog.canonical(a.class_id) AND cl.kind='class' AND cl.school_id=a.school_id
 JOIN catalog.objects tc ON tc.id=catalog.canonical(cl.term_course_id) AND tc.kind='term_course' AND tc.school_id=a.school_id
 WHERE a.kind='assignment' AND a.teacher_id IN(SELECT id FROM teacher_ids) AND tc.course_id IN(SELECT id FROM course_ids))`, teachers, course).Scan(&yes)
	return yes, safe(err)
}
