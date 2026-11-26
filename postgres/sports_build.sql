INSERT INTO catalog.projection_sports_sources(version,course_id)
SELECT $1,co.id FROM catalog.projection_objects co WHERE co.version=$1 AND co.kind='course'
AND (lower(btrim(co.name))=ANY($4::text[]) OR co.category_id=$3 OR co.id IN (
 SELECT v.course_id FROM catalog.projection_objects v WHERE v.version=$1 AND v.category_id=$3));
-- step
WITH codes AS (SELECT * FROM jsonb_to_recordset($5::jsonb) AS x(code text,id text,name text)),
sports_classes AS MATERIALIZED (
 SELECT cl.id,cl.course_id,cl.term_id,cl.data->>'sports_project_code' AS code
 FROM catalog.projection_objects cl JOIN catalog.projection_sports_sources src ON src.version=$1 AND src.course_id=cl.course_id
 WHERE cl.version=$1 AND cl.kind='class'
), source_class_ids AS (
 SELECT id,id AS source_id FROM sports_classes
 UNION ALL SELECT cl.id,r.alias_id FROM sports_classes cl JOIN catalog.redirects r ON r.canonical_id=cl.id
), taught_classes AS MATERIALIZED (
 SELECT DISTINCT cl.id AS class_id FROM source_class_ids cl
 JOIN catalog.objects a ON a.class_id=cl.source_id AND a.kind='assignment' AND a.school_id=$2
 LEFT JOIN catalog.redirects teacher_ref ON teacher_ref.alias_id=a.teacher_id
 JOIN catalog.projection_objects te ON te.version=$1 AND te.id=COALESCE(teacher_ref.canonical_id,a.teacher_id) AND te.kind='teacher'
 WHERE NOT COALESCE((a.data->'object'->>'inactive')::boolean,false)
 AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=a.id)
)
INSERT INTO catalog.projection_sports_classes(version,class_id,source_course_id,term_id,project_id,offering_id,reason)
SELECT $1,cl.id,cl.course_id,cl.term_id,COALESCE(m.id,''),
 CASE WHEN m.id IS NOT NULL THEN 'sports:offering:'||substr(encode(sha256(convert_to(m.id,'UTF8')||decode('00','hex')||convert_to(cl.term_id,'UTF8')),'hex'),1,32) ELSE '' END,
 CASE WHEN COALESCE(cl.code,'')='' THEN 'missing_project_code'
 WHEN m.id IS NULL THEN 'unknown_project_code'
 WHEN NOT EXISTS(SELECT 1 FROM taught_classes a WHERE a.class_id=cl.id) THEN 'missing_teacher' ELSE '' END
FROM sports_classes cl LEFT JOIN codes m ON m.code=cl.code;
-- step
WITH names AS(SELECT DISTINCT id,name FROM jsonb_to_recordset($5::jsonb) AS x(code text,id text,name text)),
projects AS(SELECT m.project_id,CASE WHEN count(DISTINCT cl.department_id)=1 THEN min(cl.department_id) ELSE '' END department_id,
 max(cl.term_key) term_key FROM catalog.projection_sports_classes m JOIN catalog.projection_objects cl ON cl.version=$1 AND cl.id=m.class_id
 WHERE m.version=$1 AND m.reason='' GROUP BY m.project_id)
INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,term_key,visible,data)
SELECT $1,p.project_id,$2,'course',n.name,p.department_id,$3,p.term_key,true,
 jsonb_build_object('id',p.project_id,'kind','course','school_id',$2::text,'name',n.name,'course_type','sports_project',
 'department_id',p.department_id,'category_id',$3::text,'credits',NULL,'aliases','[]'::jsonb,'revision',$1::bigint,'updated_at',g.built_at,
 'latest_term_id',(SELECT m.term_id FROM catalog.projection_sports_classes m JOIN catalog.projection_objects cl ON cl.version=$1 AND cl.id=m.class_id
 WHERE m.version=$1 AND m.project_id=p.project_id AND m.reason='' ORDER BY cl.term_key DESC,m.term_id LIMIT 1))
FROM projects p JOIN names n ON n.id=p.project_id JOIN catalog.projection_generations g ON g.version=$1;
-- step
INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,course_id,term_id,term_key,visible,data)
SELECT DISTINCT $1,m.offering_id,$2,'term_course',co.name,co.department_id,$3,m.project_id,m.term_id,cl.term_key,true,
 jsonb_build_object('id',m.offering_id,'kind','term_course','school_id',$2::text,'name',co.name,'course_id',m.project_id,'term_id',m.term_id,
 'department_id',co.department_id,'category_id',$3::text,'credits',NULL,'aliases','[]'::jsonb,'revision',$1::bigint,'updated_at',g.built_at)
FROM catalog.projection_sports_classes m JOIN catalog.projection_objects cl ON cl.version=$1 AND cl.id=m.class_id
JOIN catalog.projection_objects co ON co.version=$1 AND co.id=m.project_id
JOIN catalog.projection_generations g ON g.version=$1 WHERE m.version=$1 AND m.reason='';
-- step
UPDATE catalog.projection_objects o SET course_id=m.project_id,term_course_id=m.offering_id,category_id=$3,
 data=(o.data-'sports_project_code')||jsonb_build_object('course_id',m.project_id,'term_course_id',m.offering_id,'category_id',$3::text)
FROM catalog.projection_sports_classes m WHERE m.version=$1 AND m.reason='' AND o.version=$1
AND o.kind='class' AND o.id=m.class_id;
-- step
DELETE FROM catalog.projection_objects o WHERE o.version=$1 AND (o.id IN(SELECT course_id FROM catalog.projection_sports_sources WHERE version=$1)
 OR o.course_id IN(SELECT course_id FROM catalog.projection_sports_sources WHERE version=$1));
-- step
WITH codes AS (SELECT * FROM jsonb_to_recordset($5::jsonb) AS x(code text,id text,name text)),
coded_classes AS MATERIALIZED (
 SELECT cl.id,cl.term_course_id,cl.category_id,m.id AS project_id
 FROM catalog.objects cl
 JOIN codes m ON m.code=cl.data->'object'->>'sports_project_code'
 JOIN catalog.projection_objects target ON target.version=$1 AND target.id=m.id AND target.visible
 WHERE cl.school_id=$2 AND cl.kind='class'
), classes AS MATERIALIZED (
 SELECT cl.id,cl.project_id
 FROM coded_classes cl
 LEFT JOIN catalog.redirects offering_ref ON offering_ref.alias_id=cl.term_course_id
 JOIN catalog.objects tc ON tc.id=COALESCE(offering_ref.canonical_id,cl.term_course_id) AND tc.kind='term_course' AND tc.school_id=$2
 LEFT JOIN catalog.redirects course_ref ON course_ref.alias_id=tc.course_id
 JOIN catalog.objects co ON co.id=COALESCE(course_ref.canonical_id,tc.course_id) AND co.kind='course' AND co.school_id=$2
 CROSS JOIN LATERAL (VALUES (COALESCE(NULLIF(cl.category_id,''),NULLIF(tc.category_id,''),co.category_id))) raw(category_id)
 LEFT JOIN catalog.redirects category_ref ON category_ref.alias_id=raw.category_id
 LEFT JOIN catalog.objects category ON category.id=COALESCE(category_ref.canonical_id,raw.category_id) AND category.kind='category'
 WHERE lower(btrim(co.name))=ANY($4::text[]) OR lower(btrim(category.name))=ANY($6::text[])
)
INSERT INTO catalog.projection_teaching_history(version,teacher_id,course_id)
SELECT DISTINCT $1,COALESCE(teacher_ref.canonical_id,a.teacher_id),cl.project_id
FROM classes cl JOIN catalog.objects a ON a.class_id=cl.id AND a.kind='assignment' AND a.school_id=$2
LEFT JOIN catalog.redirects teacher_ref ON teacher_ref.alias_id=a.teacher_id
ON CONFLICT DO NOTHING;
