package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) OwnReviewCandidates(owner, school string) ([]c.ReviewCourseCandidate, error) {
	if t.projectionRead {
		rows, err := list[c.ReviewCourseCandidate](t, `SELECT jsonb_build_object('course_id',co.id,'course_name',co.name,'term_id',tm.id,'term_name',tm.name)
 FROM catalog.enrollments en JOIN catalog.projection_heads h ON h.school_id=en.school_id
 LEFT JOIN catalog.projection_aliases ca ON ca.version=h.version AND ca.alias_id=en.course_id
 LEFT JOIN catalog.projection_aliases ta ON ta.version=h.version AND ta.alias_id=en.term_id
 JOIN catalog.projection_objects co ON co.version=h.version AND co.id=COALESCE(ca.canonical_id,en.course_id) AND co.kind='course' AND co.visible
 JOIN catalog.projection_objects tm ON tm.version=h.version AND tm.id=COALESCE(ta.canonical_id,en.term_id) AND tm.kind='term' AND tm.visible
 WHERE en.subject=$1 AND en.school_id=$2 AND en.active
 AND EXISTS(SELECT 1 FROM catalog.projection_variants v WHERE v.version=h.version AND v.course_id=co.id AND v.term_id=tm.id)
 GROUP BY co.id,co.name,tm.id,tm.name,tm.term_key ORDER BY tm.term_key DESC,co.id LIMIT 2001`, owner, school)
		if err == nil && len(rows) > 2000 {
			return nil, c.ErrUnavailable
		}
		return rows, err
	}
	rows, err := list[c.ReviewCourseCandidate](t, `SELECT DISTINCT jsonb_build_object('course_id',co.id,'course_name',co.name,'term_id',term.id,'term_name',term.name)
 FROM catalog.enrollments en JOIN catalog.objects co ON co.id=catalog.canonical(en.course_id) AND co.kind='course' AND co.school_id=en.school_id
 JOIN catalog.objects term ON term.id=catalog.canonical(en.term_id) AND term.kind='term' AND term.school_id=en.school_id
 WHERE en.subject=$1 AND en.school_id=$2 AND en.active AND NOT COALESCE((co.data->'object'->>'inactive')::boolean,false)
 AND NOT COALESCE((term.data->'object'->>'inactive')::boolean,false) LIMIT 2001`, owner, school)
	if err == nil && len(rows) > 2000 {
		return nil, c.ErrUnavailable
	}
	return rows, err
}
