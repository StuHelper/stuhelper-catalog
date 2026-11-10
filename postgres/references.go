package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) OwnedReferences(subject string, ids []string) ([]string, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT requested.id FROM unnest($2::text[]) requested(id)
 WHERE EXISTS(SELECT 1 FROM catalog.enrollments en WHERE en.subject=$1
 AND (requested.id IN (en.school_id,en.course_id,en.term_id,COALESCE(en.data->>'class_id',''))
 OR COALESCE(en.data->'teacher_ids','[]'::jsonb) ? requested.id)) ORDER BY requested.id`, subject, ids)
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
 FROM unnest($1::text[]) requested(id) JOIN catalog.objects o ON o.id=catalog.canonical(requested.id) ORDER BY requested.id`, ids)
}
