package postgres

import catalog "github.com/StuHelper/stuhelper-catalog"

// Filters follow the same offering and teaching class; independent EXISTS clauses
// must not combine one term's category with another term's teacher.
func (t *transaction) List(f catalog.Filter) ([]catalog.Object, error) {
	return list[catalog.Object](t, `SELECT o.data->'object' FROM catalog.objects o
 LEFT JOIN catalog.objects parent_class ON parent_class.id=o.class_id AND parent_class.kind='class'
 LEFT JOIN catalog.objects parent_offering ON parent_offering.id=COALESCE(NULLIF(o.term_course_id,''),parent_class.term_course_id) AND parent_offering.kind='term_course'
 WHERE o.kind=$1 AND ($2='' OR o.school_id=$2) AND o.id>$3
 AND ($4='' OR strpos(lower(o.name),lower($4))>0 OR strpos(lower(o.data->'object'->>'code'),lower($4))>0
   OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(o.data->'object'->'aliases') alias WHERE strpos(lower(alias),lower($4))>0))
 AND ($5='' OR o.course_id=$5 OR (o.kind='course' AND o.id=$5) OR parent_offering.course_id=$5
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id JOIN catalog.objects tc ON tc.id=cl.term_course_id
     WHERE a.kind='assignment' AND a.teacher_id=o.id AND tc.course_id=$5 AND ($7='' OR tc.term_id=$7))))
 AND (
  (o.kind='course' AND EXISTS(
   SELECT 1 FROM catalog.objects tc LEFT JOIN catalog.objects cl ON cl.term_course_id=tc.id AND cl.kind='class'
   WHERE tc.kind='term_course' AND tc.course_id=o.id AND ($7='' OR tc.term_id=$7)
   AND ($6='' OR COALESCE(NULLIF(cl.department_id,''),NULLIF(tc.department_id,''),o.department_id)=$6)
   AND ($8='' OR COALESCE(NULLIF(cl.category_id,''),NULLIF(tc.category_id,''),o.category_id)=$8)
   AND ($9='' OR COALESCE(NULLIF(cl.nature_id,''),NULLIF(tc.nature_id,''),o.nature_id)=$9)
   AND ($10='' OR EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.class_id=cl.id AND a.teacher_id=$10))))
  OR (o.kind='course' AND $7='' AND $10='' AND ($6='' OR o.department_id=$6) AND ($8='' OR o.category_id=$8) AND ($9='' OR o.nature_id=$9))
  OR (o.kind<>'course' AND ($6='' OR o.department_id=$6) AND ($8='' OR o.category_id=$8) AND ($9='' OR o.nature_id=$9)
   AND ($7='' OR o.term_id=$7 OR (o.kind='term' AND o.id=$7) OR parent_offering.term_id=$7
     OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id JOIN catalog.objects tc ON tc.id=cl.term_course_id
       WHERE a.kind='assignment' AND a.teacher_id=o.id AND tc.term_id=$7 AND ($5='' OR tc.course_id=$5))))
   AND ($10='' OR o.teacher_id=$10 OR (o.kind='teacher' AND o.id=$10)
     OR EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id
       WHERE a.kind='assignment' AND a.teacher_id=$10 AND ((o.kind='class' AND a.class_id=o.id) OR (o.kind='schedule' AND a.class_id=o.class_id) OR (o.kind='term_course' AND cl.term_course_id=o.id)))))
 )
 AND ($12='' OR o.term_course_id=$12 OR o.id=$12 OR parent_class.term_course_id=$12
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a JOIN catalog.objects cl ON cl.id=a.class_id WHERE a.kind='assignment' AND a.teacher_id=o.id AND cl.term_course_id=$12)))
 AND ($13='' OR o.class_id=$13 OR o.id=$13
   OR (o.kind='teacher' AND EXISTS(SELECT 1 FROM catalog.objects a WHERE a.kind='assignment' AND a.teacher_id=o.id AND a.class_id=$13)))
 ORDER BY o.id LIMIT $11`, f.Kind, f.SchoolID, f.After, f.Query, f.CourseID, f.DepartmentID, f.TermID, f.CategoryID, f.NatureID, f.TeacherID, f.Limit+1, f.TermCourseID, f.ClassID)
}

func (t *transaction) Lookup(ids []string) ([]catalog.Object, error) {
	return list[catalog.Object](t, `SELECT data->'object' FROM catalog.objects WHERE id=ANY($1::text[]) ORDER BY id`, ids)
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
	err = t.tx.QueryRowContext(t.ctx, `SELECT count(DISTINCT tc.course_id), count(DISTINCT a.teacher_id), count(DISTINCT tc.id)
 FROM catalog.objects tc LEFT JOIN catalog.objects cl ON cl.term_course_id=tc.id AND cl.kind='class'
 LEFT JOIN catalog.objects a ON a.class_id=cl.id AND a.kind='assignment'
 WHERE tc.kind='term_course' AND (($2='course' AND tc.course_id=$1) OR ($2='teacher' AND a.teacher_id=$1))`, object.ID, object.Kind).Scan(&out.CourseCount, &out.TeacherCount, &out.OfferingCount)
	if err != nil {
		return out, safe(err)
	}
	if object.Kind == "course" {
		refs, err = list[catalog.Object](t, `WITH related AS (
 SELECT category_id,nature_id FROM catalog.objects WHERE id=$1
 UNION SELECT tc.category_id,tc.nature_id FROM catalog.objects tc WHERE tc.kind='term_course' AND tc.course_id=$1
 UNION SELECT cl.category_id,cl.nature_id FROM catalog.objects cl JOIN catalog.objects tc ON tc.id=cl.term_course_id WHERE cl.kind='class' AND tc.course_id=$1
 ) SELECT data->'object' FROM catalog.objects WHERE (kind='category' AND id IN (SELECT category_id FROM related)) OR (kind='nature' AND id IN (SELECT nature_id FROM related)) ORDER BY id LIMIT 1001`, object.ID)
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
