package postgres

import (
	"fmt"
	catalog "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

// Build only predicates used by this page. A single optional-filter statement
// retains unrelated joins/subplans and can trigger expensive JIT compilation.
// Values remain bind parameters; column names below are module-owned constants.
func directoryPageQuery(f catalog.Filter, prefix, requireTeacherDirectory bool) (string, []any) {
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
	if f.Query != "" {
		v := bind(f.Query)
		search = "(strpos(lower(o.name),lower(" + v + "))>0 OR strpos(lower(o.data->'object'->>'code'),lower(" + v + "))>0 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(o.data->'object'->'aliases') a WHERE strpos(lower(a),lower(" + v + "))>0))"
		if !prefix {
			where = append(where, "catalog.search_tokens(o.data->'object') @> catalog.search_tokens(jsonb_build_object('name',"+v+"::text))", search)
		}
	}

	fields := []struct{ column, value string }{{"department_id", f.DepartmentID}, {"category_id", f.CategoryID}, {"nature_id", f.NatureID}}
	if f.Kind == "course" {
		direct, related := []string{}, []string{"tc.kind='term_course'", "tc.course_id=ANY(" + keys("o.id") + ")"}
		if f.TeacherID != "" {
			teacher := bind(f.TeacherID)
			where = append(where, `o.id IN (SELECT catalog.canonical(tc.course_id) FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class' JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course' WHERE a.kind='assignment' AND a.teacher_id=ANY(`+keys(teacher)+`))`)
			related = append(related, `EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.class_id=ANY(`+keys("cl.id")+`) AND a.teacher_id=ANY(`+keys(teacher)+`))`)
		}
		for _, field := range fields {
			if field.value == "" {
				continue
			}
			value := bind(field.value)
			if field.column != "nature_id" && f.TeacherID == "" {
				where = append(where, directoryClassificationCandidates(field.column, value))
			}
			direct = append(direct, "o."+field.column+"="+value)
			related = append(related, "COALESCE(NULLIF(cl."+field.column+",''),NULLIF(tc."+field.column+",''),o."+field.column+")="+value)
		}
		if len(direct) > 0 || f.TeacherID != "" {
			prefix := ""
			if f.TeacherID == "" {
				prefix = strings.Join(direct, " AND ") + " OR "
			}
			where = append(where, "("+prefix+"EXISTS(SELECT 1 FROM catalog.objects tc LEFT JOIN catalog.objects cl ON cl.kind='class' AND cl.term_course_id=ANY(ARRAY(SELECT tc.id WHERE NOT EXISTS(SELECT 1 FROM catalog.redirects WHERE alias_id=tc.id) UNION ALL SELECT alias_id FROM catalog.redirects WHERE canonical_id=tc.id)) WHERE "+strings.Join(related, " AND ")+"))")
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
	query := "SELECT catalog.public_object(o.data->'object') FROM catalog.objects o WHERE " + strings.Join(where, " AND ") + " ORDER BY o.id LIMIT " + limit
	if prefix {
		// A frequent one-character term can match almost the entire directory.
		// Probe a bounded ID-ordered prefix before materializing every GIN match.
		query = "SELECT catalog.public_object(o.data->'object') FROM (SELECT o.id,o.name,o.data FROM catalog.objects o WHERE " + strings.Join(where, " AND ") + " ORDER BY o.id LIMIT 128) o WHERE " + search + " ORDER BY o.id LIMIT " + limit
	}
	return query, args
}

// A matching effective classification must occur on the course, offering or
// class. This indexable superset narrows candidates; the full predicate above
// still checks inheritance and all filters on the same offering/class.
func directoryClassificationCandidates(column, value string) string {
	return `o.id IN (
 SELECT DISTINCT COALESCE(cr.canonical_id, candidate.id) FROM (
 SELECT id FROM catalog.objects WHERE kind='course' AND ` + column + `<>'' AND ` + column + `=` + value + `
 UNION ALL
 SELECT course_id FROM catalog.objects WHERE kind='term_course' AND ` + column + `<>'' AND ` + column + `=` + value + `
 UNION ALL
 SELECT tc.course_id FROM catalog.objects cl
 LEFT JOIN catalog.redirects r ON r.alias_id=cl.term_course_id
 JOIN catalog.objects tc ON tc.id=COALESCE(r.canonical_id,cl.term_course_id) AND tc.kind='term_course'
 WHERE cl.kind='class' AND cl.` + column + `<>'' AND cl.` + column + `=` + value + `
 ) candidate LEFT JOIN catalog.redirects cr ON cr.alias_id=candidate.id)`
}
