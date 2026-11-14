package postgres

import catalog "github.com/StuHelper/stuhelper-catalog"

// Restrict a relation page from the selected end of the relation first. The
// complete predicates in List still enforce classification and same-term
// semantics. Resolving only matching keys avoids a correlated scan of every
// offering for a teacher with few or no assignments.
func directoryCandidates(f catalog.Filter) string {
	query := ""
	if f.Kind == "term_course" && f.CourseID != "" {
		query += ` AND o.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5)`
	}
	if f.Kind == "term_course" && f.TeacherID != "" {
		query += ` AND o.id IN (
 SELECT catalog.canonical(cl.term_course_id) FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
 WHERE a.kind='assignment' AND a.teacher_id IN (SELECT $10::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$10))`
	}
	if f.Kind == "course" && f.TeacherID != "" {
		query += ` AND o.id IN (
 SELECT catalog.canonical(tc.course_id) FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
 JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
 WHERE a.kind='assignment' AND a.teacher_id IN (SELECT $10::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$10))`
	}
	if f.Kind == "teacher" && (f.CourseID != "" || f.ClassID != "" || f.TermCourseID != "") {
		query += ` AND o.id IN (
 SELECT catalog.canonical(a.teacher_id) FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
 JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
 WHERE a.kind='assignment'
 AND ($5='' OR tc.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5))
 AND ($12='' OR cl.term_course_id IN (SELECT $12::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$12))
 AND ($13='' OR a.class_id=$13))`
	}
	return query
}
