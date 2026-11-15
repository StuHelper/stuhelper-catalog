package postgres

import (
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

func directorySelectionQuery(q c.DirectoryQuery) (string, []any, error) {
	args := []any{}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	school := bind(q.SchoolID)
	kind := "'course'"
	if q.Kind == "teacher" {
		kind = "'teacher'"
	}
	conditions := []string{"o.school_id=" + school, "o.kind=" + kind, "NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)", "NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)"}
	if q.Kind == "teacher" {
		conditions = append(conditions, markedTeacherSQL("o"))
	}
	if q.Query != "" {
		search := bind(q.Query)
		conditions = append(conditions, "catalog.search_tokens(o.data->'object') @> catalog.search_tokens(jsonb_build_object('name',"+search+"::text))",
			"(strpos(lower(o.name),lower("+search+"))>0 OR strpos(lower(o.data->'object'->>'code'),lower("+search+"))>0 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(o.data->'object'->'aliases','null'::jsonb),'[]'::jsonb)) a WHERE strpos(lower(a),lower("+search+"))>0))")
	}
	common := []string{"TRUE"}
	for _, field := range []struct{ name, value string }{{"department_id", q.DepartmentID}, {"nature_id", q.NatureID}} {
		if field.value != "" {
			value := bind(field.value)
			common = append(common, "v."+field.name+"="+value)
			if q.Kind == "course" && q.TeacherID == "" {
				conditions = append(conditions, directoryClassificationCandidates(field.name, value))
			} else if q.Kind == "teacher" {
				conditions = append(conditions, "o."+field.name+"="+value)
			}
		}
	}
	category := "TRUE"
	if q.CategoryID != "" {
		category = "category_id=" + bind(q.CategoryID)
	}
	base := "base AS MATERIALIZED (SELECT o.id,o.name,o.department_id,o.category_id,o.nature_id FROM catalog.objects o WHERE " + strings.Join(conditions, " AND ") + ")"
	variants := "variants AS (SELECT id,department_id,category_id,nature_id FROM base)"
	if q.Kind == "course" {
		direct := "SELECT id,department_id,category_id,nature_id FROM base UNION ALL "
		teacherFilter := ""
		if q.TeacherID != "" {
			teacher := bind(q.TeacherID)
			direct = ""
			teacherFilter = ` WHERE EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.school_id=` + school + `
 AND a.teacher_id IN (SELECT ` + teacher + `::text UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=` + teacher + `)
 AND a.class_id IN (SELECT cl.id UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=cl.id)
 AND NOT COALESCE((a.data->'object'->>'inactive')::boolean,false))`
		}
		variants = `course_keys AS (SELECT id,id AS source_id FROM base UNION ALL SELECT b.id,r.alias_id FROM base b JOIN catalog.redirects r ON r.canonical_id=b.id),
 offerings AS (SELECT ck.id AS course_id,tc.id,tc.department_id,tc.category_id,tc.nature_id FROM catalog.objects tc JOIN course_keys ck ON ck.source_id=tc.course_id
 WHERE tc.school_id=` + school + ` AND tc.kind='term_course' AND NOT COALESCE((tc.data->'object'->>'inactive')::boolean,false)),
 classes AS (SELECT cl.id,COALESCE(r.canonical_id,cl.term_course_id) AS term_course_id,cl.department_id,cl.category_id,cl.nature_id
 FROM catalog.objects cl LEFT JOIN catalog.redirects r ON r.alias_id=cl.term_course_id
 WHERE cl.school_id=` + school + ` AND cl.kind='class' AND NOT COALESCE((cl.data->'object'->>'inactive')::boolean,false)),
 variants AS (` + direct + `SELECT b.id,
 COALESCE(NULLIF(cl.department_id,''),NULLIF(tc.department_id,''),b.department_id) AS department_id,
 COALESCE(NULLIF(cl.category_id,''),NULLIF(tc.category_id,''),b.category_id) AS category_id,
 COALESCE(NULLIF(cl.nature_id,''),NULLIF(tc.nature_id,''),b.nature_id) AS nature_id
 FROM base b JOIN offerings tc ON tc.course_id=b.id LEFT JOIN classes cl ON cl.term_course_id=tc.id` + teacherFilter + `)`
	}
	ctes := []string{base, variants,
		"eligible AS MATERIALIZED (SELECT v.id,v.department_id,v.category_id FROM variants v WHERE " + strings.Join(common, " AND ") + " LIMIT 500001)",
		"matching AS MATERIALIZED (SELECT * FROM eligible WHERE " + category + ")",
		`candidates AS (SELECT m.id,b.name,array_agg(DISTINCT m.department_id) AS departments,array_agg(DISTINCT m.category_id) AS categories
 FROM matching m JOIN base b ON b.id=m.id GROUP BY m.id,b.name ORDER BY m.id LIMIT 100001)`,
		`category_counts AS (SELECT category_id,count(DISTINCT id) AS n FROM eligible GROUP BY category_id)`,
		`department_counts AS (SELECT department_id,count(DISTINCT id) AS n FROM matching GROUP BY department_id)`,
	}
	query := "WITH " + strings.Join(ctes, ",\n") + ` SELECT jsonb_build_object(
 'variant_count',(SELECT count(*) FROM eligible),'candidates',COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM candidates v),'[]'::jsonb),
 'facet_total',(SELECT count(DISTINCT id) FROM eligible),
 'category_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',ca.id,'name',ca.name,'count',COALESCE(cc.n,0),'review_count',0) ORDER BY ca.name COLLATE "C",ca.id)
 FROM catalog.objects ca LEFT JOIN category_counts cc ON cc.category_id=ca.id WHERE ca.kind='category' AND ca.school_id=` + school + ` AND ` + kind + `='course'
 AND NOT COALESCE((ca.data->'object'->>'inactive')::boolean,false)),'[]'::jsonb),
 'department_facets',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',dc.department_id,'name',COALESCE(d.name,''),'count',dc.n,'review_count',0) ORDER BY d.name COLLATE "C",dc.department_id)
 FROM department_counts dc LEFT JOIN catalog.objects d ON d.id=dc.department_id AND d.kind='department' AND d.school_id=` + school + `),'[]'::jsonb))`
	return query, args, nil
}
