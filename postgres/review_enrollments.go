package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) ReviewEnrollments(subject, school, course, term, after string, limit int) ([]c.Enrollment, error) {
	return list[c.Enrollment](t, `WITH owned AS MATERIALIZED(
 SELECT en.id,en.data,m.project_id,cl.term_key,v.teacher_ids
 FROM catalog.enrollments en JOIN catalog.projection_heads h ON h.school_id=en.school_id
 JOIN catalog.projection_sports_classes m ON m.version=h.version AND m.class_id=catalog.canonical(COALESCE(en.data->>'class_id',''))
 AND m.source_course_id=catalog.canonical(en.course_id) AND m.term_id=catalog.canonical(en.term_id) AND m.reason=''
 JOIN catalog.projection_objects cl ON cl.version=h.version AND cl.id=m.class_id AND cl.kind='class' AND cl.visible
 JOIN catalog.projection_variants v ON v.version=h.version AND v.course_id=m.project_id AND v.class_id=m.class_id
 WHERE en.subject=$1 AND en.school_id=$2 AND en.active AND m.project_id=$3 AND ($4='' OR m.term_id=catalog.canonical($4))
 ) SELECT data||jsonb_build_object('course_id',project_id,'term_order_key',term_key,'teacher_ids',
 (SELECT COALESCE(jsonb_agg(DISTINCT catalog.canonical(value)),'[]'::jsonb)
 FROM jsonb_array_elements_text(COALESCE(NULLIF(data->'teacher_ids','null'::jsonb),'[]'::jsonb))
 WHERE catalog.canonical(value)=ANY(teacher_ids)))
 FROM owned WHERE $5='' OR term_key<(SELECT term_key FROM owned WHERE id=$5)
 OR (term_key=(SELECT term_key FROM owned WHERE id=$5) AND id>$5)
 ORDER BY term_key DESC,id LIMIT $6`, subject, school, course, term, after, limit)
}

func (t *transaction) CourseTermTeachers(school, course, term string) ([]string, error) {
	if _, err := t.projectionVersion(school); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT DISTINCT o.id FROM catalog.projection_heads h
 JOIN catalog.projection_relations r ON r.version=h.version
 JOIN catalog.projection_objects o ON o.version=h.version AND o.id=r.teacher_id AND o.kind='teacher'
 WHERE h.school_id=$1 AND r.course_id=catalog.canonical($2) AND r.term_id=catalog.canonical($3) AND `+projectedVisible("o")+` ORDER BY o.id LIMIT 1001`, school, course, term)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, safe(err)
		}
		ids = append(ids, id)
	}
	if len(ids) > 1000 {
		return nil, c.ErrUnavailable
	}
	return ids, safe(rows.Err())
}
