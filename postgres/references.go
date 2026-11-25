package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) OwnedReferences(subject, school string, ids []string) ([]string, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT requested.id FROM unnest($2::text[]) requested(id)
 WHERE EXISTS(SELECT 1 FROM catalog.enrollments en WHERE en.subject=$1 AND en.school_id=$3
 AND (requested.id IN (en.school_id,en.course_id,en.term_id,COALESCE(en.data->>'class_id','')) OR catalog.canonical(requested.id)=catalog.canonical(en.course_id)
 OR COALESCE(en.data->'teacher_ids','[]'::jsonb) ? requested.id)) OR EXISTS(SELECT 1 FROM catalog.enrollments en JOIN catalog.projection_heads h ON h.school_id=en.school_id
 JOIN catalog.projection_sports_classes m ON m.version=h.version AND m.class_id=catalog.canonical(COALESCE(en.data->>'class_id',''))
 AND m.source_course_id=catalog.canonical(en.course_id) AND m.term_id=catalog.canonical(en.term_id) AND m.reason=''
 WHERE en.subject=$1 AND en.school_id=$3 AND m.project_id=requested.id) ORDER BY requested.id`, subject, ids, school)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, safe(e)
		}
		out = append(out, id)
	}
	return out, safe(rows.Err())
}
func (t *transaction) ReferenceNames(ids []string) ([]c.Reference, error) {
	return list[c.Reference](t, `SELECT jsonb_build_object('id',requested.id,'canonical_id',o.id,'kind',o.kind,'name',o.name)
 FROM unnest($1::text[]) requested(id) JOIN catalog.objects o ON o.id=catalog.canonical(requested.id)
 UNION ALL SELECT jsonb_build_object('id',p.id,'canonical_id',p.id,'kind',p.kind,'name',p.name)
 FROM catalog.projection_heads h JOIN catalog.projection_objects p ON p.version=h.version AND p.id=ANY($1::text[])
 WHERE p.data->>'course_type'='sports_project' AND NOT EXISTS(SELECT 1 FROM catalog.objects o WHERE o.id=p.id)`, ids)
}
