package postgres

import (
	"errors"
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) PublicObject(id string) (c.Object, error) {
	if t.projectionRead {
		return t.projectedObject(id)
	}
	obj, err := read[c.Object](t, `SELECT (catalog.public_object(o.data->'object') || catalog.address_field(o.id)) || CASE WHEN o.kind='term' THEN jsonb_build_object('term_order_key',COALESCE(tr.sort_key,0)) ELSE '{}'::jsonb END || CASE WHEN o.id<>$1 THEN jsonb_build_object('id',$1,'canonical_id',o.id) ELSE '{}'::jsonb END FROM catalog.objects o LEFT JOIN catalog.term_orders tr ON tr.id=o.id AND tr.school_id=o.school_id WHERE o.id=catalog.canonical($1)`+t.teacherVisibility("o"), id)
	if errors.Is(err, c.ErrNotFound) && c.TeacherMaintenanceRead(t.ctx) {
		return t.maintenanceProjectedObject(id)
	}
	return obj, err
}

func (t *transaction) maintenanceProjectedObject(id string) (c.Object, error) {
	return read[c.Object](t, `SELECT o.data || catalog.address_field(o.id) || CASE WHEN o.id<>$1 THEN jsonb_build_object('id',$1,'canonical_id',o.id) ELSE '{}'::jsonb END`+projectedObjectFrom+`
 WHERE o.id=COALESCE((SELECT canonical_id FROM catalog.projection_aliases a WHERE a.version=h.version AND a.alias_id=$1),$1)
   AND NOT EXISTS(SELECT 1 FROM catalog.objects src WHERE src.id=o.id)`, id)
}

func (t *transaction) projectedResolveIDs(object c.Object) ([]string, error) {
	canonical := object.ID
	if object.CanonicalID != "" {
		canonical = object.CanonicalID
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT $2::text AS id UNION SELECT a.alias_id FROM catalog.projection_heads h JOIN catalog.projection_aliases a ON a.version=h.version
 WHERE h.school_id=$1 AND a.canonical_id=$2 ORDER BY id LIMIT 1001`, object.SchoolID, canonical)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, safe(err)
		}
		out = append(out, item)
	}
	if len(out) > 1000 {
		return nil, c.ErrUnavailable
	}
	return out, safe(rows.Err())
}

func (t *transaction) ResolveIDs(id string) ([]string, error) {
	if t.projectionRead {
		object, err := t.projectedObject(id)
		if err != nil {
			return nil, err
		}
		return t.projectedResolveIDs(object)
	}
	rows, e := t.tx.QueryContext(t.ctx, `SELECT id FROM catalog.objects WHERE id=catalog.canonical($1) UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=catalog.canonical($1) ORDER BY id LIMIT 1001`, id)
	if e != nil {
		return nil, safe(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var item string
		if e = rows.Scan(&item); e != nil {
			return nil, safe(e)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, safe(err)
	}
	rows.Close()
	if len(out) > 1000 {
		return nil, c.ErrUnavailable
	}
	if len(out) == 0 && c.TeacherMaintenanceRead(t.ctx) {
		object, err := t.maintenanceProjectedObject(id)
		if err != nil {
			if errors.Is(err, c.ErrNotFound) {
				return out, nil
			}
			return nil, err
		}
		return t.projectedResolveIDs(object)
	}
	return out, nil
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
	if at.After(t.mutationTime) {
		t.mutationTime = at
	}
	if err := t.touchProjection(school); err != nil {
		return 0, err
	}
	count := 0
	if source != "" {
		if e := t.redirect(source, target, at, reason); e != nil {
			return 0, e
		}
		count++
	}
	// The raw relation keys are already unique. Canonical collisions can only
	// exist after a redirect; fresh source pages need no full-school window scan.
	var redirected bool
	if e := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.redirects r JOIN catalog.objects o ON o.id=r.alias_id WHERE o.school_id=$1)`, school).Scan(&redirected); e != nil {
		return 0, safe(e)
	}
	if !redirected {
		return count, nil
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

func (t *transaction) AddressObjectID(school, kind, address string) (string, error) {
	var id string
	err := t.tx.QueryRowContext(t.ctx, `SELECT object_id FROM catalog.addresses WHERE school_id=$1 AND kind=$2 AND address=$3`, school, kind, address).Scan(&id)
	return id, safe(err)
}
