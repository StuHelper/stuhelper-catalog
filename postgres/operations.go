package postgres

import (
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) PublicObject(id string) (c.Object, error) {
	return read[c.Object](t, `SELECT catalog.public_object(data->'object') || CASE WHEN id<>$1 THEN jsonb_build_object('id',$1,'canonical_id',id) ELSE '{}'::jsonb END FROM catalog.objects WHERE id=catalog.canonical($1)`, id)
}
func (t *transaction) ResolveIDs(id string) ([]string, error) {
	rows, e := t.tx.QueryContext(t.ctx, `SELECT id FROM catalog.objects WHERE id=catalog.canonical($1) UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=catalog.canonical($1) ORDER BY id LIMIT 1001`, id)
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
	if len(out) > 1000 {
		return nil, c.ErrUnavailable
	}
	return out, safe(rows.Err())
}
func (t *transaction) redirect(source, target string, at time.Time, reason string) error {
	_, e := t.tx.ExecContext(t.ctx, `UPDATE catalog.redirects SET canonical_id=$2 WHERE canonical_id=$1`, source, target)
	if e != nil {
		return safe(e)
	}
	_, e = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.redirects(alias_id,canonical_id,reason,created_at) VALUES($1,$2,$3,$4)`, source, target, reason, at)
	return safe(e)
}
func (t *transaction) MergeAliases(source, target, school string, at time.Time, reason string) (int, error) {
	count := 0
	if source != "" {
		if e := t.redirect(source, target, at, reason); e != nil {
			return 0, e
		}
		count++
	}
	for _, kind := range []string{"term_course", "assignment"} {
		var q string
		if kind == "term_course" {
			q = `SELECT id,first_value(id) OVER(PARTITION BY catalog.canonical(course_id),term_id ORDER BY (course_id=catalog.canonical(course_id)) DESC,id) AS canonical FROM catalog.objects o WHERE school_id=$1 AND kind='term_course' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)`
		} else {
			q = `SELECT id,first_value(id) OVER(PARTITION BY catalog.canonical(class_id),catalog.canonical(teacher_id) ORDER BY id) AS canonical FROM catalog.objects o WHERE school_id=$1 AND kind='assignment' AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)`
		}
		rows, e := t.tx.QueryContext(t.ctx, `SELECT id,canonical FROM (`+q+`) x WHERE id<>canonical LIMIT 1001`, school)
		if e != nil {
			return 0, safe(e)
		}
		pairs := [][2]string{}
		for rows.Next() {
			var pair [2]string
			if e = rows.Scan(&pair[0], &pair[1]); e != nil {
				rows.Close()
				return 0, safe(e)
			}
			pairs = append(pairs, pair)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return 0, safe(e)
		}
		if len(pairs) > 1000 {
			return 0, c.ErrUnavailable
		}
		for _, pair := range pairs {
			if e = t.redirect(pair[0], pair[1], at, reason); e != nil {
				return 0, e
			}
			count++
		}
	}
	return count, nil
}
