-- Inline this school-scoped relation into each build stage. The planner can
-- discard unused left joins because each redirect has a unique alias_id.
SELECT o.id,o.school_id,o.kind,o.name,o.data,o.department_id AS source_department_id,
 COALESCE(department_ref.canonical_id,o.department_id) AS department_id,
 COALESCE(category_ref.canonical_id,o.category_id) AS category_id,
 COALESCE(nature_ref.canonical_id,o.nature_id) AS nature_id,
 COALESCE(course_ref.canonical_id,o.course_id) AS course_id,
 COALESCE(term_ref.canonical_id,o.term_id) AS term_id,
 COALESCE(term_course_ref.canonical_id,o.term_course_id) AS term_course_id,
 COALESCE(class_ref.canonical_id,o.class_id) AS class_id,
 COALESCE(teacher_ref.canonical_id,o.teacher_id) AS teacher_id,
 (o.data->'object'||jsonb_build_object(
  'course_id',COALESCE(jcourse.canonical_id,o.data->'object'->>'course_id',''),
  'teacher_id',COALESCE(jteacher.canonical_id,o.data->'object'->>'teacher_id',''),
  'term_course_id',COALESCE(joffering.canonical_id,o.data->'object'->>'term_course_id',''),
  'class_id',COALESCE(jclass.canonical_id,o.data->'object'->>'class_id',''))) AS public_data
FROM catalog.objects o
LEFT JOIN catalog.redirects department_ref ON department_ref.alias_id=o.department_id
LEFT JOIN catalog.redirects category_ref ON category_ref.alias_id=o.category_id
LEFT JOIN catalog.redirects nature_ref ON nature_ref.alias_id=o.nature_id
LEFT JOIN catalog.redirects course_ref ON course_ref.alias_id=o.course_id
LEFT JOIN catalog.redirects term_ref ON term_ref.alias_id=o.term_id
LEFT JOIN catalog.redirects term_course_ref ON term_course_ref.alias_id=o.term_course_id
LEFT JOIN catalog.redirects class_ref ON class_ref.alias_id=o.class_id
LEFT JOIN catalog.redirects teacher_ref ON teacher_ref.alias_id=o.teacher_id
LEFT JOIN catalog.redirects jcourse ON jcourse.alias_id=o.data->'object'->>'course_id'
LEFT JOIN catalog.redirects jteacher ON jteacher.alias_id=o.data->'object'->>'teacher_id'
LEFT JOIN catalog.redirects joffering ON joffering.alias_id=o.data->'object'->>'term_course_id'
LEFT JOIN catalog.redirects jclass ON jclass.alias_id=o.data->'object'->>'class_id'
WHERE o.school_id=$1 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id)
