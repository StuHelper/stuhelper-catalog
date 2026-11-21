package postgres

import (
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"strings"
)

// Every expansion joins its immutable generation, including aliases and labels.
// The caller's current content authorization remains in the catalog Service.
const projectedObjectFrom = ` FROM catalog.projection_heads h
 JOIN catalog.projection_objects o ON o.version=h.version`

// A manual decision can revoke the only evidence for an old projected teacher
// before the replacement generation is ready. Current official evidence is
// checked during that interval; the published teaching cutoff still applies.
func projectedVisible(alias string) string {
	return alias + `.visible AND (` + alias + `.kind<>'teacher' OR ` + alias + `.teaching
 OR NOT EXISTS(SELECT 1 FROM catalog.projection_pending p JOIN catalog.teacher_homepage_review_state rs USING(school_id)
 WHERE p.school_id=` + alias + `.school_id)
 OR EXISTS(SELECT 1 FROM catalog.objects src JOIN catalog.teacher_directory td ON td.teacher_id=src.id AND td.school_id=src.school_id
 WHERE src.id=` + alias + `.id AND src.school_id=` + alias + `.school_id AND src.id=catalog.canonical(src.id)
 AND NOT COALESCE((src.data->'object'->>'inactive')::boolean,false) AND ` + officialTeacherCondition("td", "src") + `))`
}

func (t *transaction) projectedObject(id string) (c.Object, error) {
	return read[c.Object](t, `SELECT o.data || CASE WHEN o.id<>$1 THEN jsonb_build_object('id',$1,'canonical_id',o.id) ELSE '{}'::jsonb END`+projectedObjectFrom+`
 WHERE o.id=COALESCE((SELECT canonical_id FROM catalog.projection_aliases a WHERE a.version=h.version AND a.alias_id=$1),$1) AND `+projectedVisible("o"), id)
}
func (t *transaction) projectedLookup(ids []string) ([]c.Object, error) {
	return list[c.Object](t, `SELECT o.data || CASE WHEN o.id<>requested.id THEN jsonb_build_object('id',requested.id,'canonical_id',o.id) ELSE '{}'::jsonb END
 FROM catalog.projection_heads h CROSS JOIN unnest($1::text[]) requested(id)
 LEFT JOIN catalog.projection_aliases a ON a.version=h.version AND a.alias_id=requested.id
 JOIN catalog.projection_objects o ON o.version=h.version AND o.id=COALESCE(a.canonical_id,requested.id)
 WHERE `+projectedVisible("o")+` ORDER BY requested.id`, ids)
}
func (t *transaction) projectedSummary(object c.Object) (c.Summary, error) {
	return read[c.Summary](t, `SELECT s.data || jsonb_build_object('object',(s.data->'object') ||
 CASE WHEN s.id<>$2 THEN jsonb_build_object('id',$2,'canonical_id',s.id) ELSE '{}'::jsonb END)
 FROM catalog.projection_heads h LEFT JOIN catalog.projection_aliases a ON a.version=h.version AND a.alias_id=$2
 JOIN catalog.projection_summaries s ON s.version=h.version AND s.id=COALESCE(a.canonical_id,$2) WHERE h.school_id=$1`, object.SchoolID, object.ID)
}
func (t *transaction) projectedMarks(school string) (c.TeacherDirectoryResult, error) {
	return read[c.TeacherDirectoryResult](t, `SELECT g.report->'marks' FROM catalog.projection_heads h JOIN catalog.projection_generations g ON g.version=h.version WHERE h.school_id=$1`, school)
}

func (t *transaction) projectedList(f c.Filter) ([]c.Object, error) {
	if f.SchoolID != "" {
		if _, err := t.projectionVersion(f.SchoolID); err != nil {
			return nil, err
		}
	}
	args := []any{}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	canonical := func(id string) string {
		p := bind(id)
		return `COALESCE((SELECT canonical_id FROM catalog.projection_aliases a WHERE a.version=o.version AND a.alias_id=` + p + `),` + p + `)`
	}
	where := []string{"o.visible", "o.kind=" + bind(f.Kind)}
	if f.Kind == "teacher" {
		where[0] = projectedVisible("o")
	}
	if f.SchoolID != "" {
		where = append(where, "h.school_id="+bind(f.SchoolID))
	}
	if f.IDs != nil {
		where = append(where, "o.id=ANY("+bind(f.IDs)+"::text[])")
	}
	if f.Query != "" {
		p := bind(f.Query)
		where = append(where, `catalog.matches_query(o.data,`+p+`)`)
	}
	order := "o.id"
	if f.Kind == "term" {
		order = "o.term_key DESC,o.id"
	}
	if f.After != "" {
		p := bind(f.After)
		if f.Kind == "term" {
			where = append(where, `(o.term_key<(SELECT term_key FROM catalog.projection_objects prev WHERE prev.version=o.version AND prev.id=`+p+`) OR (o.term_key=(SELECT term_key FROM catalog.projection_objects prev WHERE prev.version=o.version AND prev.id=`+p+`) AND o.id>`+p+`))`)
		} else {
			where = append(where, "o.id>"+p)
		}
	}
	relation := map[string]string{"course": "v.course_id=o.id", "teacher": "o.id=ANY(v.teacher_ids)", "term": "v.term_id=o.id", "term_course": "v.offering_id=o.id", "class": "v.class_id=o.id", "assignment": "v.class_id=o.class_id", "schedule": "v.class_id=o.class_id"}[f.Kind]
	conditions := []string{"v.version=o.version", relation}
	for _, field := range []struct{ column, value string }{{"course_id", f.CourseID}, {"term_id", f.TermID}, {"offering_id", f.TermCourseID}, {"class_id", f.ClassID}} {
		if field.value != "" {
			conditions = append(conditions, "v."+field.column+"="+canonical(field.value))
		}
	}
	for _, field := range []struct{ column, value string }{{"department_id", f.DepartmentID}, {"category_id", f.CategoryID}, {"nature_id", f.NatureID}} {
		if field.value != "" {
			value := canonical(field.value)
			if f.Kind == "teacher" {
				where = append(where, "o."+field.column+"="+value)
			} else {
				conditions = append(conditions, "v."+field.column+"="+value)
			}
		}
	}
	if f.TeacherID != "" {
		value := canonical(f.TeacherID)
		if f.Kind == "teacher" {
			where = append(where, "o.id="+value)
		} else {
			conditions = append(conditions, "v.teacher_ids @> ARRAY["+value+"]::text[]")
			if f.Kind == "assignment" {
				where = append(where, "o.teacher_id="+value)
			}
		}
	}
	if len(conditions) > 2 {
		if relation == "" {
			return []c.Object{}, nil
		}
		where = append(where, `EXISTS(SELECT 1 FROM catalog.projection_variants v WHERE `+strings.Join(conditions, " AND ")+`)`)
	}
	return list[c.Object](t, "SELECT o.data"+projectedObjectFrom+" WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order+" LIMIT "+bind(f.Limit+1), args...)
}

func (t *transaction) projectedFilterOptions(kind, school string) (c.FilterOptions, error) {
	out, err := read[c.FilterOptions](t, `SELECT jsonb_build_object('kind',$2::text,
 'total',(g.report->>CASE WHEN $2='course' THEN 'courses' ELSE 'teachers' END)::bigint,
 'departments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name,'count',count) ORDER BY name COLLATE "C",id) FROM catalog.projection_options WHERE version=h.version AND owner_kind=$2 AND kind='department'),'[]'::jsonb),
 'categories',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name,'count',count) ORDER BY name COLLATE "C",id) FROM catalog.projection_options WHERE version=h.version AND owner_kind=$2 AND kind='category'),'[]'::jsonb),
 'natures',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'name',name,'count',count) ORDER BY name COLLATE "C",id) FROM catalog.projection_options WHERE version=h.version AND owner_kind=$2 AND kind='nature'),'[]'::jsonb))
 FROM catalog.projection_heads h JOIN catalog.projection_generations g ON g.version=h.version WHERE h.school_id=$1`, school, kind)
	if errors.Is(err, c.ErrNotFound) {
		err = c.ErrUnavailable
	}
	return out, err
}
