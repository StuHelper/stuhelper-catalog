INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,nature_id,term_key,official,data)
SELECT $2,o.id,o.school_id,o.kind,o.name,catalog.canonical(o.department_id),catalog.canonical(o.category_id),catalog.canonical(o.nature_id),
 COALESCE(t.sort_key,0),COALESCE(td.official AND (td.homepage_manual OR (td.matched_name=o.name AND td.matched_department_id=o.department_id)),false),
 catalog.public_object(o.data->'object')-'term_order_key'-'latest_term_id'
FROM catalog.objects o LEFT JOIN catalog.term_orders t ON t.id=o.id AND t.school_id=o.school_id
LEFT JOIN catalog.teacher_directory td ON td.teacher_id=o.id
WHERE o.school_id=$1 AND o.kind IN('school','department','category','nature','term','course','teacher')
AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id);
-- step
INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_key,visible,data)
SELECT $2,o.id,o.school_id,o.kind,o.name,
 COALESCE(NULLIF(catalog.canonical(o.department_id),''),co.department_id),
 COALESCE(NULLIF(catalog.canonical(o.category_id),''),co.category_id),
 COALESCE(NULLIF(catalog.canonical(o.nature_id),''),co.nature_id),co.id,tm.id,tm.term_key,true,
 catalog.public_object(o.data->'object')
FROM catalog.objects o
JOIN catalog.projection_objects co ON co.version=$2 AND co.id=catalog.canonical(o.course_id) AND co.kind='course'
JOIN catalog.projection_objects tm ON tm.version=$2 AND tm.id=catalog.canonical(o.term_id) AND tm.kind='term' AND tm.term_key>=$3
WHERE o.school_id=$1 AND o.kind='term_course' AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id);
-- step
UPDATE catalog.projection_objects co SET visible=true WHERE co.version=$2 AND co.kind='course'
AND EXISTS(SELECT 1 FROM catalog.projection_objects tc WHERE tc.version=$2 AND tc.kind='term_course' AND tc.course_id=co.id);
-- step
DELETE FROM catalog.projection_objects WHERE version=$2 AND kind='course' AND NOT visible;
-- step
UPDATE catalog.projection_objects co SET term_key=latest.term_key,data=co.data||jsonb_build_object('latest_term_id',latest.term_id)
FROM (SELECT DISTINCT ON(course_id) course_id,term_id,term_key FROM catalog.projection_objects
 WHERE version=$2 AND kind='term_course' ORDER BY course_id,term_key DESC,term_id) latest
WHERE co.version=$2 AND co.id=latest.course_id AND co.kind='course';
-- step
INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,term_key,visible,data)
SELECT $2,o.id,o.school_id,o.kind,o.name,
 COALESCE(NULLIF(catalog.canonical(o.department_id),''),tc.department_id),
 COALESCE(NULLIF(catalog.canonical(o.category_id),''),tc.category_id),
 COALESCE(NULLIF(catalog.canonical(o.nature_id),''),tc.nature_id),tc.course_id,tc.term_id,tc.id,tc.term_key,true,
 catalog.public_object(o.data->'object')
FROM catalog.objects o JOIN catalog.projection_objects tc ON tc.version=$2 AND tc.id=catalog.canonical(o.term_course_id) AND tc.kind='term_course'
WHERE o.school_id=$1 AND o.kind='class' AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id);
-- step
INSERT INTO catalog.projection_objects(version,id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,term_key,visible,data)
SELECT $2,o.id,o.school_id,o.kind,o.name,cl.department_id,cl.category_id,cl.nature_id,cl.course_id,cl.term_id,cl.term_course_id,cl.id,
 CASE WHEN o.kind='assignment' THEN te.id ELSE '' END,cl.term_key,true,catalog.public_object(o.data->'object')
FROM catalog.objects o JOIN catalog.projection_objects cl ON cl.version=$2 AND cl.id=catalog.canonical(o.class_id) AND cl.kind='class'
LEFT JOIN catalog.projection_objects te ON te.version=$2 AND te.id=catalog.canonical(o.teacher_id) AND te.kind='teacher'
WHERE o.school_id=$1 AND o.kind IN('assignment','schedule') AND (o.kind='schedule' OR te.id IS NOT NULL)
AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
AND NOT EXISTS(SELECT 1 FROM catalog.redirects r WHERE r.alias_id=o.id);
-- step
UPDATE catalog.projection_objects te SET teaching=EXISTS(SELECT 1 FROM catalog.projection_objects a
 WHERE a.version=$2 AND a.kind='assignment' AND a.teacher_id=te.id)
WHERE te.version=$2 AND te.kind='teacher';
-- step
UPDATE catalog.projection_objects SET visible=(teaching OR official) WHERE version=$2 AND kind='teacher';
-- step
INSERT INTO catalog.projection_variants(version,course_id,offering_id,class_id,term_id,term_key,department_id,category_id,nature_id,teacher_ids)
SELECT $2,tc.course_id,tc.id,COALESCE(cl.id,''),tc.term_id,tc.term_key,
 COALESCE(cl.department_id,tc.department_id),COALESCE(cl.category_id,tc.category_id),COALESCE(cl.nature_id,tc.nature_id),
 COALESCE(a.teachers,'{}'::text[])
FROM catalog.projection_objects tc
LEFT JOIN catalog.projection_objects cl ON cl.version=$2 AND cl.kind='class' AND cl.term_course_id=tc.id
LEFT JOIN (SELECT class_id,array_agg(DISTINCT teacher_id ORDER BY teacher_id) AS teachers
 FROM catalog.projection_objects WHERE version=$2 AND kind='assignment' GROUP BY class_id) a ON a.class_id=cl.id
WHERE tc.version=$2 AND tc.kind='term_course';
-- step
INSERT INTO catalog.projection_variants(version,course_id,offering_id,class_id,term_id,term_key,department_id,category_id,nature_id)
SELECT $2,id,'','','',0,department_id,category_id,nature_id
FROM catalog.projection_objects WHERE version=$2 AND kind='course' AND visible;
-- step
INSERT INTO catalog.projection_relations(version,course_id,teacher_id,term_id,offering_id)
SELECT DISTINCT $2,course_id,teacher_id,term_id,term_course_id FROM catalog.projection_objects WHERE version=$2 AND kind='assignment';
-- step
INSERT INTO catalog.projection_options(version,owner_kind,kind,id,name,count,term_key)
SELECT $2,'course',k.kind,o.id,o.name,count(DISTINCT v.course_id),o.term_key
FROM catalog.projection_variants v
CROSS JOIN LATERAL(VALUES('department',v.department_id),('category',v.category_id),('nature',v.nature_id),('term',v.term_id)) k(kind,id)
JOIN catalog.projection_objects o ON o.version=$2 AND o.id=k.id AND o.kind=k.kind
WHERE v.version=$2 GROUP BY k.kind,o.id,o.name,o.term_key;
-- step
INSERT INTO catalog.projection_options(version,owner_kind,kind,id,name,count)
SELECT $2,'teacher','department',d.id,d.name,count(*) FROM catalog.projection_objects te
JOIN catalog.projection_objects d ON d.version=$2 AND d.kind='department' AND d.id=te.department_id
WHERE te.version=$2 AND te.kind='teacher' AND te.visible GROUP BY d.id,d.name;
-- step
UPDATE catalog.projection_objects o SET visible=(kind='school' OR EXISTS(SELECT 1 FROM catalog.projection_options opt WHERE opt.version=$2 AND opt.id=o.id))
WHERE o.version=$2 AND o.kind IN('school','department','category','nature','term');
-- step
UPDATE catalog.projection_objects SET data=data || jsonb_build_object('term_order_key',term_key)
WHERE version=$2 AND kind='term' AND term_key>0;
-- step
INSERT INTO catalog.projection_aliases(version,alias_id,canonical_id)
SELECT $2,r.alias_id,COALESCE((SELECT canonical_id FROM catalog.projection_aliases a WHERE a.version=$2 AND a.alias_id=r.canonical_id),r.canonical_id) FROM catalog.redirects r JOIN catalog.objects o ON o.id=r.canonical_id AND o.school_id=$1 ON CONFLICT(version,alias_id) DO NOTHING;
-- step
WITH counts AS (
 SELECT course_id AS id,count(DISTINCT offering_id) FILTER(WHERE offering_id<>'') AS offerings
 FROM catalog.projection_variants WHERE version=$2 GROUP BY course_id
), teachers AS (
 SELECT course_id AS id,count(DISTINCT teacher_id) AS n FROM catalog.projection_relations WHERE version=$2 GROUP BY course_id
), courses AS (
 SELECT teacher_id AS id,count(DISTINCT course_id) AS n,count(DISTINCT offering_id) AS offerings
 FROM catalog.projection_relations WHERE version=$2 GROUP BY teacher_id
), pairs AS (
 SELECT DISTINCT r.course_id,co.name AS course_name,r.teacher_id,te.name AS teacher_name
 FROM catalog.projection_relations r
 JOIN catalog.projection_objects co ON co.version=$2 AND co.id=r.course_id AND co.kind='course' AND co.visible
 JOIN catalog.projection_objects te ON te.version=$2 AND te.id=r.teacher_id AND te.kind='teacher' AND te.visible
 WHERE r.version=$2
), teacher_refs AS (
 SELECT course_id AS id,jsonb_agg(jsonb_build_object('id',teacher_id,'canonical_id',teacher_id,'kind','teacher','name',teacher_name) ORDER BY teacher_id) AS items
 FROM (SELECT course_id,teacher_id,teacher_name,row_number() OVER(PARTITION BY course_id ORDER BY teacher_id) AS rn FROM pairs) ranked
 WHERE rn<=20 GROUP BY course_id
), course_refs AS (
 SELECT teacher_id AS id,jsonb_agg(jsonb_build_object('id',course_id,'canonical_id',course_id,'kind','course','name',course_name) ORDER BY course_id) AS items
 FROM (SELECT teacher_id,course_id,course_name,row_number() OVER(PARTITION BY teacher_id ORDER BY course_id) AS rn FROM pairs) ranked
 WHERE rn<=20 GROUP BY teacher_id
), classifications AS (
 SELECT v.course_id,o.kind,jsonb_agg(DISTINCT o.data ORDER BY o.data) AS items
 FROM catalog.projection_variants v CROSS JOIN LATERAL(VALUES(v.category_id),(v.nature_id)) k(id)
 JOIN catalog.projection_objects o ON o.version=$2 AND o.id=k.id WHERE v.version=$2 GROUP BY v.course_id,o.kind
)
INSERT INTO catalog.projection_summaries(version,id,data)
SELECT $2,o.id,jsonb_build_object('object',o.data,'school',s.data,'department',d.data,
 'categories',COALESCE(ca.items,'[]'::jsonb),'natures',COALESCE(na.items,'[]'::jsonb),
 'courses',CASE WHEN o.kind='teacher' THEN COALESCE(cr.items,'[]'::jsonb) ELSE '[]'::jsonb END,
 'teachers',CASE WHEN o.kind='course' THEN COALESCE(tr.items,'[]'::jsonb) ELSE '[]'::jsonb END,
 'course_count',CASE WHEN o.kind='course' THEN CASE WHEN o.visible THEN 1 ELSE 0 END ELSE COALESCE(co.n,0) END,
 'teacher_count',CASE WHEN o.kind='teacher' THEN CASE WHEN o.teaching THEN 1 ELSE 0 END ELSE COALESCE(te.n,0) END,
 'offering_count',COALESCE(ct.offerings,co.offerings,0))
FROM catalog.projection_objects o
JOIN catalog.projection_objects s ON s.version=$2 AND s.id=$1 AND s.kind='school'
LEFT JOIN catalog.projection_objects d ON d.version=$2 AND d.id=o.department_id AND d.kind='department'
LEFT JOIN counts ct ON ct.id=o.id LEFT JOIN teachers te ON te.id=o.id LEFT JOIN courses co ON co.id=o.id
LEFT JOIN teacher_refs tr ON tr.id=o.id LEFT JOIN course_refs cr ON cr.id=o.id
LEFT JOIN classifications ca ON ca.course_id=o.id AND ca.kind='category'
LEFT JOIN classifications na ON na.course_id=o.id AND na.kind='nature'
WHERE o.version=$2 AND o.kind IN('course','teacher') AND o.visible;
-- step
INSERT INTO catalog.projection_documents(version,id,data)
SELECT $2,o.id,jsonb_build_object('target',jsonb_build_object('source','catalog','id',o.id,'kind',o.kind),
 'revision',$2,'title',CASE WHEN o.visible THEN o.name ELSE '' END,'school_id',o.school_id,'visible',o.visible,
 'code',CASE WHEN o.kind IN('school','course') AND o.visible THEN COALESCE(o.data->>'code','') ELSE '' END,
 'aliases',CASE WHEN o.kind IN('school','course') AND o.visible THEN o.data->'aliases' ELSE '[]'::jsonb END,
 'english_name',CASE WHEN o.kind='course' AND o.visible THEN COALESCE(o.data->>'english_name','') ELSE '' END,
 'former_codes',CASE WHEN o.kind='course' AND o.visible THEN COALESCE(o.data->'former_codes','[]'::jsonb) ELSE '[]'::jsonb END,
 'former_names',CASE WHEN o.kind='course' AND o.visible THEN COALESCE(o.data->'former_names','[]'::jsonb) ELSE '[]'::jsonb END,
 'ratings','[0,0,0,0,0]'::jsonb,'at',g.built_at)
FROM catalog.projection_objects o JOIN catalog.projection_generations g ON g.version=$2
WHERE o.version=$2 AND o.kind IN('school','course','teacher');
-- step
INSERT INTO catalog.projection_documents(version,id,data)
SELECT $2,old.id,old.data || jsonb_build_object('revision',$2,'title','','visible',false,'code','','aliases','[]'::jsonb,'english_name','','former_codes','[]'::jsonb,'former_names','[]'::jsonb,'at',g.built_at)
FROM catalog.projection_heads h JOIN catalog.projection_documents old ON old.version=h.version
JOIN catalog.projection_generations g ON g.version=$2
WHERE h.school_id=$1 AND NOT EXISTS(SELECT 1 FROM catalog.projection_documents n WHERE n.version=$2 AND n.id=old.id);
-- step
INSERT INTO catalog.projection_teaching_history(version,teacher_id,course_id)
SELECT DISTINCT $2,catalog.canonical(a.teacher_id),catalog.canonical(tc.course_id)
FROM catalog.objects a
JOIN catalog.objects cl ON cl.id=catalog.canonical(a.class_id) AND cl.kind='class' AND cl.school_id=a.school_id
JOIN catalog.objects tc ON tc.id=catalog.canonical(cl.term_course_id) AND tc.kind='term_course' AND tc.school_id=a.school_id
JOIN catalog.projection_objects co ON co.version=$2 AND co.kind='course' AND co.visible AND co.id=catalog.canonical(tc.course_id)
WHERE a.kind='assignment' AND a.school_id=$1;

-- step
INSERT INTO catalog.projection_teaching_history(version,teacher_id,course_id)
SELECT DISTINCT $2,r.teacher_id,r.course_id FROM catalog.projection_relations r
JOIN catalog.projection_objects co ON co.version=$2 AND co.id=r.course_id AND co.data->>'course_type'='sports_project'
WHERE r.version=$2 ON CONFLICT DO NOTHING;
