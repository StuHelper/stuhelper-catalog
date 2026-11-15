package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) SummaryAliases(school, kind string) (map[string]string, error) {
	query := `SELECT r.alias_id,r.canonical_id FROM catalog.redirects r
 JOIN catalog.objects o ON o.id=r.canonical_id WHERE o.school_id=$1 AND o.kind=$2 ORDER BY r.alias_id LIMIT 100001`
	if t.projectionRead {
		query = `SELECT r.alias_id,r.canonical_id FROM catalog.projection_heads h JOIN catalog.projection_aliases r ON r.version=h.version
 JOIN catalog.projection_objects o ON o.version=h.version AND o.id=r.canonical_id WHERE h.school_id=$1 AND o.kind=$2 ORDER BY r.alias_id LIMIT 100001`
	}
	rows, err := t.tx.QueryContext(t.ctx, query, school, kind)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, canonical string
		if err := rows.Scan(&id, &canonical); err != nil {
			return nil, safe(err)
		}
		out[id] = canonical
		if len(out) > 100000 {
			return nil, c.ErrUnavailable
		}
	}
	return out, safe(rows.Err())
}
