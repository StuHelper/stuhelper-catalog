package postgres

import catalog "github.com/StuHelper/stuhelper-catalog"

// Filters follow the same offering and teaching class; independent EXISTS clauses
// must not combine one term's category with another term's teacher.
func (t *transaction) List(f catalog.Filter) ([]catalog.Object, error) {
	return list[catalog.Object](t, `SELECT catalog.public_object(o.data->'object') FROM catalog.objects o
 LEFT JOIN catalog.objects parent_class ON parent_class.id=o.class_id AND parent_class.kind='class'
 LEFT JOIN catalog.redirects parent_redirect ON parent_redirect.alias_id=COALESCE(NULLIF(o.term_course_id,''),parent_class.term_course_id)
 LEFT JOIN catalog.objects parent_offering ON parent_offering.id=COALESCE(parent_redirect.canonical_id,NULLIF(o.term_course_id,''),parent_class.term_course_id) AND parent_offering.kind='term_course'
 WHERE o.kind=$1 AND NOT EXISTS(SELECT 1 FROM catalog.redirects redirect WHERE redirect.alias_id=o.id) AND ($14 OR NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)) AND ($2='' OR o.school_id=$2) AND o.id>$3
 AND ($15::text[] IS NULL OR o.id=ANY($15))
 AND ($4='' OR CASE WHEN o.kind='school' AND $4 ~ '^[0-9]+$' THEN
   o.id=$4 OR o.data->'object'->>'code'=$4
   OR (length($4)=5 AND ((o.id ~ '^[0-9]{10}$' AND right(o.id,5)=$4)
     OR (o.data->'object'->>'code' ~ '^[0-9]{10}$' AND right(o.data->'object'->>'code',5)=$4)))
 ELSE strpos(lower(o.name),lower($4))>0 OR strpos(lower(o.data->'object'->>'code'),lower($4))>0
   OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(o.data->'object'->'aliases') alias WHERE strpos(lower(alias),lower($4))>0) END)

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
 `+directoryCandidates(f)+` ORDER BY o.id LIMIT $11`, f.Kind, f.SchoolID, f.After, f.Query, f.CourseID, f.DepartmentID, f.TermID, f.CategoryID, f.NatureID, f.TeacherID, f.Limit+1, f.TermCourseID, f.ClassID, f.IncludeInactive, f.IDs)
}

func (t *transaction) Lookup(ids []string) ([]catalog.Object, error) {
	return list[catalog.Object](t, `SELECT catalog.public_object(o.data->'object') || CASE WHEN o.id<>requested.id THEN jsonb_build_object('id',requested.id,'canonical_id',o.id) ELSE '{}'::jsonb END FROM unnest($1::text[]) requested(id) JOIN catalog.objects o ON o.id=catalog.canonical(requested.id) ORDER BY requested.id`, ids)
}

func (t *transaction) Summary(object catalog.Object) (catalog.Summary, error) {
	out := catalog.Summary{Object: object, Categories: []catalog.Object{}, Natures: []catalog.Object{}}
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
