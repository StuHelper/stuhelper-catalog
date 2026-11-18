package postgres

import (
	catalog "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Filters follow the same offering and teaching class; independent EXISTS clauses
// must not combine one term's category with another term's teacher.
func (t *transaction) List(f catalog.Filter) ([]catalog.Object, error) {
	if t.projectionRead && f.Kind != "school" {
		return t.projectedList(f)
	}
	if (f.Kind == "course" || f.Kind == "teacher") && f.CourseID == "" && f.TermID == "" && f.TermCourseID == "" && f.ClassID == "" {
		// These bounded pages are latency-sensitive. LLVM compilation of the
		// relational predicates can exceed the query's work and memory budget.
		// The setting ends with this transaction; other module queries keep theirs.
		if _, err := t.tx.ExecContext(t.ctx, "SET LOCAL jit=off"); err != nil {
			return nil, safe(err)
		}
		prefixLimit := 0
		if f.Query != "" && f.DepartmentID == "" && f.CategoryID == "" && f.NatureID == "" && f.TeacherID == "" && f.IDs == nil {
			prefixLimit = 128
		} else if f.Kind == "course" && f.CategoryID != "" && f.Query == "" && f.DepartmentID == "" && f.NatureID == "" && f.TeacherID == "" && f.IDs == nil {
			// Only broad categories benefit from scanning an ordered course prefix.
			// Stop this covering-index probe after 4097 matching class keys.
			var broad bool
			if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE kind='class' AND category_id<>'' AND category_id=$1 AND ($2='' OR school_id=$2) OFFSET 4096)`, pgx.QueryExecModeExec, f.CategoryID, f.SchoolID).Scan(&broad); err != nil {
				return nil, safe(err)
			}
			if broad {
				prefixLimit = 512
			}
		}
		if prefixLimit != 0 {
			q, args := directoryPageQuery(f, prefixLimit, t.requireTeacherDirectory)
			rows, err := list[catalog.Object](t, q, append([]any{pgx.QueryExecModeExec}, args...)...)
			if err != nil {
				return nil, err
			}
			if len(rows) >= f.Limit+1 {
				return rows, nil
			}
		}
		q, args := directoryPageQuery(f, 0, t.requireTeacherDirectory)
		// Selectivity differs substantially across departments and categories.
		// Keep bind parameters, but plan each page for its actual values.
		return list[catalog.Object](t, q, append([]any{pgx.QueryExecModeExec}, args...)...)
	}
	return list[catalog.Object](t, `SELECT catalog.public_object(o.data->'object') || CASE WHEN o.kind='term' THEN jsonb_build_object('term_order_key',COALESCE(tr.sort_key,0)) ELSE '{}'::jsonb END FROM catalog.objects o
 LEFT JOIN catalog.term_orders tr ON tr.id=o.id AND tr.school_id=o.school_id
 LEFT JOIN catalog.objects parent_class ON parent_class.id=o.class_id AND parent_class.kind='class'
 LEFT JOIN catalog.redirects parent_redirect ON parent_redirect.alias_id=COALESCE(NULLIF(o.term_course_id,''),parent_class.term_course_id)
 LEFT JOIN catalog.objects parent_offering ON parent_offering.id=COALESCE(parent_redirect.canonical_id,NULLIF(o.term_course_id,''),parent_class.term_course_id) AND parent_offering.kind='term_course'
 WHERE o.kind=$1 AND NOT EXISTS(SELECT 1 FROM catalog.redirects redirect WHERE redirect.alias_id=o.id) AND ($14 OR NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)) AND ($2='' OR o.school_id=$2) AND ($3='' OR ($1<>'term' AND o.id>$3) OR ($1='term' AND (-COALESCE(tr.sort_key,0),o.id)>(-COALESCE((SELECT sort_key FROM catalog.term_orders WHERE id=catalog.canonical($3) AND school_id=o.school_id),0),$3)))
 AND ($15::text[] IS NULL OR o.id=ANY($15))
 AND ($4='' OR CASE WHEN o.kind='school' AND $4 ~ '^[0-9]+$' THEN
   o.id=$4 OR o.data->'object'->>'code'=$4
   OR (length($4)=5 AND ((o.id ~ '^[0-9]{10}$' AND right(o.id,5)=$4)
     OR (o.data->'object'->>'code' ~ '^[0-9]{10}$' AND right(o.data->'object'->>'code',5)=$4)))
 ELSE catalog.matches_query(o.data->'object',$4) END)

 AND ($5='' OR o.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5) OR (o.kind='course' AND o.id=$5) OR parent_offering.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5)
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id JOIN catalog.objects tc ON tc.id=cl.term_course_id
     WHERE a.kind='assignment' AND a.teacher_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id) AND tc.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5) AND ($7='' OR tc.term_id=$7))))
 AND (
  (o.kind='course' AND EXISTS(
   SELECT 1 FROM catalog.objects tc LEFT JOIN catalog.objects cl ON cl.term_course_id IN (SELECT tc.id::text WHERE NOT EXISTS(SELECT 1 FROM catalog.redirects WHERE alias_id=tc.id) UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=tc.id) AND cl.kind='class'
   WHERE tc.kind='term_course' AND tc.course_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id) AND ($7='' OR tc.term_id=$7)
   AND ($6='' OR COALESCE(NULLIF(cl.department_id,''),NULLIF(tc.department_id,''),o.department_id)=$6)
   AND ($8='' OR COALESCE(NULLIF(cl.category_id,''),NULLIF(tc.category_id,''),o.category_id)=$8)
   AND ($9='' OR COALESCE(NULLIF(cl.nature_id,''),NULLIF(tc.nature_id,''),o.nature_id)=$9)
   AND ($10='' OR EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.class_id IN (SELECT cl.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=cl.id) AND a.teacher_id IN (SELECT $10::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$10)))))
  OR (o.kind='course' AND $7='' AND $10='' AND ($6='' OR o.department_id=$6) AND ($8='' OR o.category_id=$8) AND ($9='' OR o.nature_id=$9))
  OR (o.kind<>'course' AND ($6='' OR o.department_id=$6) AND ($8='' OR o.category_id=$8) AND ($9='' OR o.nature_id=$9)
   AND ($7='' OR o.term_id=$7 OR (o.kind='term' AND o.id=$7) OR parent_offering.term_id=$7
     OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id JOIN catalog.objects tc ON tc.id=cl.term_course_id
       WHERE a.kind='assignment' AND a.teacher_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id) AND tc.term_id=$7 AND ($5='' OR tc.course_id IN (SELECT $5::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$5)))))
   AND ($10='' OR o.teacher_id IN (SELECT $10::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$10) OR (o.kind='teacher' AND o.id=$10)
     OR EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id
       WHERE a.kind='assignment' AND a.teacher_id IN (SELECT $10::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$10) AND ((o.kind='class' AND a.class_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id)) OR (o.kind='schedule' AND a.class_id IN (SELECT o.class_id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.class_id)) OR (o.kind='term_course' AND cl.term_course_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id))))))
 )
 AND ($12='' OR o.term_course_id IN (SELECT $12::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$12) OR o.id=$12 OR parent_class.term_course_id IN (SELECT $12::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$12)
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id WHERE a.kind='assignment' AND a.teacher_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id) AND cl.term_course_id IN (SELECT $12::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$12))))
 AND ($13='' OR o.class_id IN (SELECT $13::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$13) OR o.id=$13
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.teacher_id IN (SELECT o.id::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=o.id) AND a.class_id IN (SELECT $13::text UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$13))))
 `+directoryCandidates(f)+t.teacherVisibility("o")+` ORDER BY CASE WHEN o.kind='term' THEN COALESCE(tr.sort_key,0) ELSE 0 END DESC,o.id LIMIT $11`, f.Kind, f.SchoolID, f.After, f.Query, f.CourseID, f.DepartmentID, f.TermID, f.CategoryID, f.NatureID, f.TeacherID, f.Limit+1, f.TermCourseID, f.ClassID, f.IncludeInactive, f.IDs)
}

func (t *transaction) Lookup(ids []string) ([]catalog.Object, error) {
	if t.projectionRead {
		return t.projectedLookup(ids)
	}
	if catalog.TeacherMaintenanceRead(t.ctx) {
		return list[catalog.Object](t, `SELECT item FROM (
 SELECT requested.id AS sort_id, catalog.public_object(o.data->'object') || CASE WHEN o.kind='term' THEN jsonb_build_object('term_order_key',COALESCE(tr.sort_key,0)) ELSE '{}'::jsonb END || CASE WHEN o.id<>requested.id THEN jsonb_build_object('id',requested.id,'canonical_id',o.id) ELSE '{}'::jsonb END AS item
 FROM unnest($1::text[]) requested(id) JOIN catalog.objects o ON o.id=catalog.canonical(requested.id) LEFT JOIN catalog.term_orders tr ON tr.id=o.id AND tr.school_id=o.school_id WHERE true`+t.teacherVisibility("o")+`
 UNION ALL
 SELECT requested.id AS sort_id, o.data || CASE WHEN o.id<>requested.id THEN jsonb_build_object('id',requested.id,'canonical_id',o.id) ELSE '{}'::jsonb END AS item
 FROM catalog.projection_heads h CROSS JOIN unnest($1::text[]) requested(id)
 LEFT JOIN catalog.projection_aliases a ON a.version=h.version AND a.alias_id=requested.id
 JOIN catalog.projection_objects o ON o.version=h.version AND o.id=COALESCE(a.canonical_id,requested.id)
 WHERE NOT EXISTS(SELECT 1 FROM catalog.objects src WHERE src.id=catalog.canonical(requested.id))
   AND NOT EXISTS(SELECT 1 FROM catalog.objects src WHERE src.id=o.id)
) combined ORDER BY sort_id`, ids)
	}
	return list[catalog.Object](t, `SELECT catalog.public_object(o.data->'object') || CASE WHEN o.kind='term' THEN jsonb_build_object('term_order_key',COALESCE(tr.sort_key,0)) ELSE '{}'::jsonb END || CASE WHEN o.id<>requested.id THEN jsonb_build_object('id',requested.id,'canonical_id',o.id) ELSE '{}'::jsonb END FROM unnest($1::text[]) requested(id) JOIN catalog.objects o ON o.id=catalog.canonical(requested.id) LEFT JOIN catalog.term_orders tr ON tr.id=o.id AND tr.school_id=o.school_id WHERE true`+t.teacherVisibility("o")+` ORDER BY requested.id`, ids)
}

func (t *transaction) Summary(object catalog.Object) (catalog.Summary, error) {
	if t.projectionRead {
		return t.projectedSummary(object)
	}
	if catalog.TeacherMaintenanceRead(t.ctx) {
		var inSource bool
		if err := t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM catalog.objects WHERE id=catalog.canonical($1))`, object.ID).Scan(&inSource); err != nil {
			return catalog.Summary{}, safe(err)
		}
		if !inSource {
			return t.projectedSummary(object)
		}
	}
	out := catalog.Summary{
		Object:     object,
		Categories: []catalog.Object{},
		Natures:    []catalog.Object{},
		Courses:    []catalog.Reference{},
		Teachers:   []catalog.Reference{},
	}
	refs, err := t.Lookup([]string{object.SchoolID, object.DepartmentID})
	if err != nil {
		return out, err
	}
	for _, ref := range refs {
		if ref.ID == object.SchoolID {
			out.School = ref
		}
		if ref.ID == object.DepartmentID {
			out.Department = &ref
		}
	}
	if out.School.ID == "" {
		return out, catalog.ErrUnavailable
	}
	id := object.ID
	if object.CanonicalID != "" {
		id = object.CanonicalID
	}
	query := summaryCourseSQL
	if object.Kind == "teacher" {
		query = summaryTeacherSQL
	}
	if t.requireTeacherDirectory {
		query = strings.ReplaceAll(query, "count(DISTINCT catalog.canonical(a.teacher_id))", "count(DISTINCT CASE WHEN EXISTS(SELECT 1 FROM catalog.objects teacher WHERE teacher.id=catalog.canonical(a.teacher_id) AND "+markedTeacherSQL("teacher")+") THEN catalog.canonical(a.teacher_id) END)")
	}
	if object.Kind == "course" || object.Kind == "teacher" {
		err = t.tx.QueryRowContext(t.ctx, query, id).Scan(&out.CourseCount, &out.TeacherCount, &out.OfferingCount)
	}

	if err != nil {
		return out, safe(err)
	}
	if object.Kind == "course" {
		refs, err = list[catalog.Object](t, summaryClassificationsSQL, id)
		if err != nil {
			return out, err
		}
		if len(refs) > 1000 {
			return out, catalog.ErrUnavailable
		}
		for _, ref := range refs {
			if ref.Kind == "category" {
				out.Categories = append(out.Categories, ref)
			} else {
				out.Natures = append(out.Natures, ref)
			}
		}
		out.Teachers, err = list[catalog.Reference](t, summaryCourseTeachersSQL+t.teacherVisibility("teacher")+` ORDER BY teacher.id LIMIT 20`, id)
		if err != nil {
			return out, err
		}
	} else if object.Kind == "teacher" {
		out.Courses, err = list[catalog.Reference](t, summaryTeacherCoursesSQL, id)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (t *transaction) OwnScheduleMeetings(owner string, ids []string, limit int) ([]catalog.EnrollmentMeeting, error) {
	return list[catalog.EnrollmentMeeting](t, ownScheduleMeetingsSQL, owner, ids, limit)
}

// Resolve only the requested owner's classes before joining schedule rows.
// Materialize the bounded meeting set once before matching enrollments. This
// avoids repeating a scan for duplicate classes under skewed statistics; the
// class-id predicate remains indexable. Alias expansion preserves old links.
const ownScheduleMeetingsSQL = `WITH owned AS MATERIALIZED (
 SELECT id,school_id,catalog.canonical(COALESCE(data->>'class_id','')) AS class_id
 FROM catalog.enrollments WHERE subject=$1 AND id=ANY($2::text[]) AND active
), class_keys AS MATERIALIZED (
 SELECT id,school_id,class_id FROM owned WHERE class_id<>''
 UNION
 SELECT en.id,en.school_id,r.alias_id FROM owned en JOIN catalog.redirects r ON r.canonical_id=en.class_id
), class_ids AS MATERIALIZED (
 SELECT DISTINCT class_id FROM class_keys
), meetings AS MATERIALIZED (
 SELECT o.id,o.class_id,o.school_id,o.data FROM catalog.objects o
 WHERE o.kind='schedule' AND o.class_id=ANY(ARRAY(SELECT class_id FROM class_ids))
 AND EXISTS(SELECT 1 FROM class_keys en WHERE en.class_id=o.class_id AND en.school_id=o.school_id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 LIMIT $3
)
SELECT jsonb_build_object('EnrollmentID',en.id,'Meeting',
 jsonb_build_object('id',o.id,'time_text',COALESCE(o.data->'object'->>'time_text',''),
 'location',COALESCE(o.data->'object'->>'location',''),'schedule_time',o.data->'object'->'schedule_time'))
FROM class_keys en JOIN meetings o ON o.class_id=en.class_id AND o.school_id=en.school_id
ORDER BY en.id,o.id LIMIT $3`

// Merges preserve original records and their foreign keys. Expand the target's
// alias set first, then join those keys directly so relation indexes remain
// usable. Canonicalization is needed only for the resulting aggregate values.
const summaryCourseSQL = `WITH course_ids AS (
 SELECT $1::text AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$1
)
SELECT count(DISTINCT catalog.canonical(tc.course_id)), count(DISTINCT catalog.canonical(a.teacher_id)), count(DISTINCT catalog.canonical(tc.id))
FROM catalog.objects tc
LEFT JOIN catalog.objects cl ON cl.term_course_id=tc.id AND cl.kind='class'
LEFT JOIN catalog.objects a ON a.class_id=cl.id AND a.kind='assignment'
WHERE tc.kind='term_course' AND tc.course_id IN (SELECT id FROM course_ids)`

const summaryTeacherSQL = `WITH teacher_ids AS (
 SELECT $1::text AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$1
)
SELECT count(DISTINCT catalog.canonical(tc.course_id)), count(DISTINCT catalog.canonical(a.teacher_id)), count(DISTINCT catalog.canonical(tc.id))
FROM catalog.objects a
JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
WHERE a.kind='assignment' AND a.teacher_id IN (SELECT id FROM teacher_ids)`

const summaryClassificationsSQL = `WITH course_ids AS (
 SELECT $1::text AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$1
), related AS (
 SELECT category_id,nature_id FROM catalog.objects WHERE id IN (SELECT id FROM course_ids)
 UNION SELECT tc.category_id,tc.nature_id FROM catalog.objects tc WHERE tc.kind='term_course' AND tc.course_id IN (SELECT id FROM course_ids)
 UNION SELECT cl.category_id,cl.nature_id FROM catalog.objects cl JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
 WHERE cl.kind='class' AND tc.course_id IN (SELECT id FROM course_ids)
), identifiers AS (
 SELECT category_id AS id,'category' AS kind FROM related
 UNION SELECT nature_id AS id,'nature' AS kind FROM related
)
SELECT o.data->'object' FROM identifiers r JOIN catalog.objects o ON o.id=r.id AND o.kind=r.kind ORDER BY o.id LIMIT 1001`

const summaryCourseTeachersSQL = `WITH course_ids AS (
 SELECT $1::text AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$1
), teacher_ids AS (
 SELECT DISTINCT catalog.canonical(a.teacher_id) AS id
 FROM catalog.objects tc
 JOIN catalog.objects cl ON cl.term_course_id=tc.id AND cl.kind='class'
 JOIN catalog.objects a ON a.class_id=cl.id AND a.kind='assignment'
 WHERE tc.kind='term_course' AND tc.course_id IN (SELECT id FROM course_ids)
)
SELECT jsonb_build_object('id',teacher.id,'canonical_id',teacher.id,'kind','teacher','name',teacher.name)
FROM teacher_ids r JOIN catalog.objects teacher ON teacher.id=r.id AND teacher.kind='teacher'
WHERE NOT COALESCE((teacher.data->'object'->>'inactive')::boolean,false)`

const summaryTeacherCoursesSQL = `WITH teacher_ids AS (
 SELECT $1::text AS id UNION SELECT alias_id FROM catalog.redirects WHERE canonical_id=$1
), course_ids AS (
 SELECT DISTINCT catalog.canonical(tc.course_id) AS id
 FROM catalog.objects a
 JOIN catalog.objects cl ON cl.id=a.class_id AND cl.kind='class'
 JOIN catalog.objects tc ON tc.id=cl.term_course_id AND tc.kind='term_course'
 WHERE a.kind='assignment' AND a.teacher_id IN (SELECT id FROM teacher_ids)
)
SELECT jsonb_build_object('id',course.id,'canonical_id',course.id,'kind','course','name',course.name)
FROM course_ids r JOIN catalog.objects course ON course.id=r.id AND course.kind='course'
WHERE NOT COALESCE((course.data->'object'->>'inactive')::boolean,false) ORDER BY course.id LIMIT 20`
