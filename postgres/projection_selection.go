package postgres

import (
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

func projectedSelectionQuery(q c.DirectoryQuery, version int64, useHomepageRevocations bool) (string, []any, error) {
	args := []any{version, q.Kind}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	canonical := func(id string) string {
		p := bind(id)
		return `COALESCE((SELECT canonical_id FROM catalog.projection_aliases WHERE version=$1 AND alias_id=` + p + `),` + p + `)`
	}
	canonicalList := func(ids []string) string {
		p := bind(ids)
		return `ARRAY(SELECT DISTINCT COALESCE(a.canonical_id,u.id) FROM unnest(` + p + `::text[]) u(id) LEFT JOIN catalog.projection_aliases a ON a.version=$1 AND a.alias_id=u.id)`
	}
	baseWhere := []string{"o.version=$1", "o.kind=$2", "o.visible"}
	if useHomepageRevocations {
		baseWhere[2] = projectedVisible("o")
	}
	if q.Query != "" {
		p := bind(q.Query)
		baseWhere = append(baseWhere, `catalog.matches_query(o.data,`+p+`)`)
	}
	if q.CreditsMin != nil {
		baseWhere = append(baseWhere, `(o.data->>'credits') IS NOT NULL AND (o.data->>'credits')::double precision>=`+bind(*q.CreditsMin))
	}
	if q.CreditsMax != nil {
		baseWhere = append(baseWhere, `(o.data->>'credits') IS NOT NULL AND (o.data->>'credits')::double precision<=`+bind(*q.CreditsMax))
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
	if len(q.DepartmentIDs) > 0 {
		common = append(common, "v.department_id=ANY("+canonicalList(q.DepartmentIDs)+")")
	} else if q.DepartmentID != "" {
		common = append(common, "v.department_id="+canonical(q.DepartmentID))
	}
	if q.NatureID != "" {
		common = append(common, "v.nature_id="+canonical(q.NatureID))
	}
	category := "TRUE"
	if len(q.CategoryIDs) > 0 {
		category = "category_id=ANY(" + canonicalList(q.CategoryIDs) + ")"
	} else if q.CategoryID != "" {
		category = "category_id=" + canonical(q.CategoryID)
	}
	if q.CategoryID != "" || len(q.CategoryIDs) > 0 {
		category += " AND category_id IN (SELECT id FROM catalog.projection_options WHERE version=$1 AND owner_kind='course' AND kind='category')"
	}
	query := `WITH base AS MATERIALIZED(SELECT o.id,o.name,o.department_id,o.category_id,o.nature_id FROM catalog.projection_objects o WHERE ` + strings.Join(baseWhere, " AND ") + `),
 variants AS (` + variants + `), eligible AS MATERIALIZED(SELECT v.* FROM variants v WHERE ` + strings.Join(common, " AND ") + `),
 matching AS MATERIALIZED(SELECT * FROM eligible WHERE ` + category + `),
 candidates AS (SELECT m.id,b.name,COALESCE(array_agg(DISTINCT m.department_id) FILTER (WHERE m.department_id<>''),'{}'::text[]) AS departments,array_agg(DISTINCT m.category_id) AS categories,array_agg(DISTINCT m.nature_id) AS natures
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
 'department_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',dc.department_id,'name',o.name,'count',dc.n,'review_count',0) ORDER BY o.department_sort_order NULLS LAST,o.name COLLATE "C",o.id)
 FROM department_counts dc JOIN catalog.projection_objects o ON o.version=$1 AND o.kind='department' AND o.id=dc.department_id),'[]'::jsonb),
 'nature_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',nc.nature_id,'name',o.name,'count',nc.n,'review_count',0) ORDER BY o.name COLLATE "C",nc.nature_id)
 FROM nature_counts nc JOIN catalog.projection_objects o ON o.version=$1 AND o.kind='nature' AND o.id=nc.nature_id),'[]'::jsonb))`
	return query, args, nil
}
