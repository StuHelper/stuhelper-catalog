package postgres

import (
	"fmt"
	catalog "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

// Build only predicates used by this page. A single optional-filter statement
// retains unrelated joins/subplans and can trigger expensive JIT compilation.
// Values remain bind parameters; column names below are module-owned constants.
func directoryPageQuery(f catalog.Filter, prefixLimit int, requireTeacherDirectory bool) (string, []any) {
	var search string
	args := []any{}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	keys := func(value string) string {
		return "ARRAY(SELECT " + value + "::text UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=" + value + ")"
	}
	kind := "'teacher'"
	if f.Kind == "course" {
		kind = "'course'"
	}
	where := []string{"o.kind=" + kind, "NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)"}
	if f.Kind == "teacher" && requireTeacherDirectory {
		where = append(where, markedTeacherSQL("o"))
	}
	if f.SchoolID != "" {
		where = append(where, "o.school_id="+bind(f.SchoolID))
	}
	if f.After != "" {
		where = append(where, "o.id>"+bind(f.After))
	}
	if !f.IncludeInactive {
		where = append(where, "NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)")
	}
	if f.IDs != nil {
		where = append(where, "o.id=ANY("+bind(f.IDs)+"::text[])")
	}
	baseWhere := append([]string(nil), where...)
	if f.Query != "" {
		v := bind(f.Query)
		search = "catalog.matches_query(o.data->'object'," + v + ")"
		if prefixLimit == 0 {
			where = append(where, "catalog.search_tokens_v2(o.data->'object') @> catalog.search_tokens_v2(jsonb_build_object('name',"+v+"::text))", search)
		}
	}

	fields := []struct{ column, value string }{{"department_id", f.DepartmentID}, {"category_id", f.CategoryID}, {"nature_id", f.NatureID}}
	if f.Kind == "course" && f.TeacherID != "" {
		teacher := bind(f.TeacherID)
		conditions := []string{}
		for _, field := range fields {
			if field.value != "" {
				conditions = append(conditions, "COALESCE(NULLIF(taught."+field.column+",''),NULLIF(taught.offering_"+field.column+",''),course."+field.column+")="+bind(field.value))
			}
		}
		where = append(where, directoryTeacherCandidates(keys(teacher), conditions))
	} else if f.Kind == "course" {
		direct, related := []string{}, []string{"tc.kind='term_course'", "tc.course_id=ANY(" + keys("o.id") + ")"}
		for _, field := range fields {
			if field.value == "" {
				continue
			}
			value := bind(field.value)
			if field.column != "nature_id" && prefixLimit == 0 {
				where = append(where, directoryClassificationCandidates(field.column, value))
			}
			direct = append(direct, "o."+field.column+"="+value)
			related = append(related, "COALESCE(NULLIF(cl."+field.column+",''),NULLIF(tc."+field.column+",''),o."+field.column+")="+value)
		}
		if len(direct) > 0 {
			where = append(where, "("+strings.Join(direct, " AND ")+" OR EXISTS(SELECT 1 FROM catalog.objects tc LEFT JOIN catalog.objects cl ON cl.kind='class' AND cl.term_course_id=ANY(ARRAY(SELECT tc.id WHERE NOT EXISTS(SELECT 1 FROM catalog.redirects WHERE alias_id=tc.id) UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=tc.id)) WHERE "+strings.Join(related, " AND ")+"))")
		}
	} else {
		if f.TeacherID != "" {
			where = append(where, "o.id="+bind(f.TeacherID))
		}
		for _, field := range fields {
			if field.value != "" {
				where = append(where, "o."+field.column+"<>'' AND o."+field.column+"="+bind(field.value))
			}
		}
	}

	limit := bind(f.Limit + 1)
	query := "SELECT (catalog.public_object(o.data->'object') || catalog.address_field(o.id)) FROM catalog.objects o WHERE " + strings.Join(where, " AND ") + " ORDER BY o.id LIMIT " + limit
	if prefixLimit != 0 {
		filters := append([]string(nil), where[len(baseWhere):]...)
		if search != "" {
			filters = append(filters, search)
		}
		// LIMIT stays inside the subquery, before relational or literal checks.
		// If this bounded prefix is insufficient, List executes the full query.
		query = "SELECT (catalog.public_object(o.data->'object') || catalog.address_field(o.id)) FROM (SELECT o.id,o.name,o.data,o.department_id,o.category_id,o.nature_id FROM catalog.objects o WHERE " + strings.Join(baseWhere, " AND ") + " ORDER BY o.id LIMIT " + fmt.Sprint(prefixLimit) + ") o WHERE " + strings.Join(filters, " AND ") + " ORDER BY o.id LIMIT " + limit
	}
	return query, args
}

// Start from the selected teacher's assignments once. A correlated offering
// recheck can make the planner scan the whole course directory for a teacher
// with few matching courses. Materialization and an array InitPlan keep that
// work independent of the outer page while preserving the same class filters.
func directoryTeacherCandidates(teacherKeys string, conditions []string) string {
	if len(conditions) == 0 {
		// No classification needs inheritance: visit relation keys once, and
		// deduplicate offerings before reading their covered course identifiers.
		return `o.id=ANY(ARRAY(WITH classes AS MATERIALIZED (
 SELECT DISTINCT class_id FROM catalog.objects WHERE kind='assignment' AND teacher_id=ANY(` + teacherKeys + `)
 ), offerings AS MATERIALIZED (
 SELECT DISTINCT cl.term_course_id FROM classes JOIN catalog.objects cl ON cl.id=classes.class_id AND cl.kind='class'
 ) SELECT DISTINCT COALESCE(r.canonical_id,tc.course_id) FROM offerings
 JOIN catalog.objects tc ON tc.id=offerings.term_course_id AND tc.kind='term_course'
 LEFT JOIN catalog.redirects r ON r.alias_id=tc.course_id))`
	}
	query := `o.id=ANY(ARRAY(WITH taught AS MATERIALIZED (
 SELECT tc.course_id,cl.department_id,cl.category_id,cl.nature_id,
 tc.department_id AS offering_department_id,tc.category_id AS offering_category_id,tc.nature_id AS offering_nature_id
 FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
 JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
 WHERE a.kind='assignment' AND a.teacher_id=ANY(` + teacherKeys + `)
 ) SELECT DISTINCT course.id FROM taught
 LEFT JOIN catalog.redirects r ON r.alias_id=taught.course_id
 JOIN catalog.objects course ON course.id=COALESCE(r.canonical_id,taught.course_id) AND course.kind='course'`
	if len(conditions) != 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	return query + "))"
}

// A matching effective classification must occur on the course, offering or
// class. This indexable superset narrows candidates; the full predicate above
// still checks inheritance and all filters on the same offering/class.
// IDs are validated ASCII keys. Internal deduplication uses byte comparisons;
// the outer lookup and cursor ordering retain the database's existing collation.
// Up to 512 offering keys use point lookups; larger sets scan the covering index
// once. OFFSET 0 prevents that broad branch becoming repeated index probes.
func directoryClassificationCandidates(column, value string) string {
	return directoryClassificationPredicate(column, "="+value)
}

func directoryClassificationCandidateList(column, values string) string {
	return directoryClassificationPredicate(column, "=ANY("+values+"::text[])")
}

func directoryClassificationPredicate(column, cmp string) string {
	return `o.id=ANY((ARRAY(
 SELECT DISTINCT COALESCE(cr.canonical_id, candidate.id) COLLATE "C" FROM (
 SELECT id FROM catalog.objects WHERE kind='course' AND ` + column + `<>'' AND ` + column + cmp + `
 UNION ALL
 SELECT course_id FROM catalog.objects WHERE kind='term_course' AND ` + column + `<>'' AND ` + column + cmp + `
 UNION ALL
 SELECT course_id FROM (
 WITH class_keys AS MATERIALIZED (
 SELECT DISTINCT COALESCE(r.canonical_id,cl.term_course_id) COLLATE "C" AS term_course_id
 FROM catalog.objects cl LEFT JOIN catalog.redirects r ON r.alias_id=cl.term_course_id
 WHERE cl.kind='class' AND cl.` + column + `<>'' AND cl.` + column + cmp + `
 ), broad AS MATERIALIZED (SELECT EXISTS(SELECT 1 FROM class_keys OFFSET 512) AS value)
 SELECT tc.course_id FROM class_keys
 JOIN catalog.objects tc ON tc.id=class_keys.term_course_id COLLATE "default" AND tc.kind='term_course'
 WHERE NOT (SELECT value FROM broad)
 UNION ALL
 SELECT tc.course_id FROM (SELECT id,course_id FROM catalog.objects WHERE kind='term_course' OFFSET 0) tc
 JOIN class_keys ON class_keys.term_course_id=tc.id COLLATE "C" WHERE (SELECT value FROM broad)
 ) classified_courses
 ) candidate LEFT JOIN catalog.redirects cr ON cr.alias_id=candidate.id)) COLLATE "default")`
}
