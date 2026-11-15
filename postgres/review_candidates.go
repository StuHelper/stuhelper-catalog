package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) OwnReviewCandidates(owner, school string) ([]c.ReviewCourseCandidate, error) {
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
