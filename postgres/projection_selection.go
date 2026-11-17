package postgres

import (
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

func projectedSelectionQuery(q c.DirectoryQuery, version int64) (string, []any, error) {
	args := []any{version, q.Kind}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	canonical := func(id string) string {
		p := bind(id)
		return `COALESCE((SELECT canonical_id FROM catalog.projection_aliases WHERE version=$1 AND alias_id=` + p + `),` + p + `)`
	}
	baseWhere := []string{"o.version=$1", "o.kind=$2", "o.visible"}
	if q.Query != "" {
		p := bind(q.Query)
		baseWhere = append(baseWhere, `catalog.matches_query(o.data,`+p+`)`)
	}
	variants := `SELECT id,department_id,category_id,nature_id FROM base`
	if q.Kind == "course" {
		where := "v.version=$1"
		if q.TeacherID != "" {
			where += " AND v.teacher_ids @> ARRAY[" + canonical(q.TeacherID) + "]::text[]"
		}
		variants = `SELECT v.course_id AS id,v.department_id,v.category_id,v.nature_id FROM catalog.projection_variants v JOIN base b ON b.id=v.course_id WHERE ` + where
	}
	common := []string{"TRUE"}
	for _, field := range []struct{ column, value string }{{"department_id", q.DepartmentID}, {"nature_id", q.NatureID}} {
		if field.value != "" {
			common = append(common, "v."+field.column+"="+canonical(field.value))
		}
	}
	category := "TRUE"
	if q.CategoryID != "" {
		category = "category_id=" + canonical(q.CategoryID)
	}
	query := `WITH base AS MATERIALIZED(SELECT o.id,o.name,o.department_id,o.category_id,o.nature_id FROM catalog.projection_objects o WHERE ` + strings.Join(baseWhere, " AND ") + `),
 variants AS (` + variants + `), eligible AS MATERIALIZED(SELECT v.* FROM variants v WHERE ` + strings.Join(common, " AND ") + `),
 matching AS MATERIALIZED(SELECT * FROM eligible WHERE ` + category + `),
 candidates AS (SELECT m.id,b.name,array_agg(DISTINCT m.department_id) AS departments,array_agg(DISTINCT m.category_id) AS categories
 FROM matching m JOIN base b ON b.id=m.id GROUP BY m.id,b.name ORDER BY m.id),
 category_counts AS (SELECT category_id,count(DISTINCT id) AS n,array_agg(DISTINCT id) AS ids FROM eligible GROUP BY category_id),
 department_counts AS (SELECT department_id,count(DISTINCT id) AS n FROM matching GROUP BY department_id),
 nature_counts AS (SELECT nature_id,count(DISTINCT id) AS n FROM matching GROUP BY nature_id)
 SELECT jsonb_build_object('variant_count',(SELECT count(*) FROM eligible),
 'candidates',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM candidates c),'[]'::jsonb),
 'facet_total',(SELECT count(DISTINCT id) FROM eligible),
 'category_members',COALESCE((SELECT jsonb_object_agg(category_id,ids) FROM category_counts),'{}'::jsonb),
 'category_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',o.id,'name',o.name,'count',COALESCE(cc.n,0),'review_count',0) ORDER BY o.name COLLATE "C",o.id)
 FROM catalog.projection_options o LEFT JOIN category_counts cc ON cc.category_id=o.id
 WHERE o.version=$1 AND o.owner_kind=$2 AND o.kind='category'),'[]'::jsonb),
 'department_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',dc.department_id,'name',COALESCE(o.name,''),'count',dc.n,'review_count',0) ORDER BY o.name COLLATE "C",dc.department_id)
 FROM department_counts dc LEFT JOIN catalog.projection_objects o ON o.version=$1 AND o.kind='department' AND o.id=dc.department_id),'[]'::jsonb),
 'nature_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',nc.nature_id,'name',o.name,'count',nc.n,'review_count',0) ORDER BY o.name COLLATE "C",nc.nature_id)
 FROM nature_counts nc JOIN catalog.projection_objects o ON o.version=$1 AND o.kind='nature' AND o.id=nc.nature_id),'[]'::jsonb))`
	return query, args, nil
}
