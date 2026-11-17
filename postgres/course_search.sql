-- Separate functions/indexes preserve rollback compatibility. Existing token
-- indexes are never reused after changing an immutable function's meaning.
CREATE OR REPLACE FUNCTION catalog.search_values(value jsonb) RETURNS SETOF text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT COALESCE(value->>'name','')
 UNION ALL SELECT COALESCE(value->>'code','')
 UNION ALL SELECT COALESCE(value->>'english_name','')
 UNION ALL SELECT a FROM jsonb_array_elements_text(
  COALESCE(NULLIF(value->'aliases','null'::jsonb),'[]'::jsonb)
  || COALESCE(NULLIF(value->'former_codes','null'::jsonb),'[]'::jsonb)
  || COALESCE(NULLIF(value->'former_names','null'::jsonb),'[]'::jsonb)) a
$$;
CREATE OR REPLACE FUNCTION catalog.matches_query(value jsonb,query text) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT EXISTS(SELECT 1 FROM catalog.search_values(value) v WHERE strpos(lower(v),lower(query))>0)
$$;
CREATE OR REPLACE FUNCTION catalog.search_tokens_v2(value jsonb) RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT COALESCE(array_agg(DISTINCT token),'{}'::text[]) FROM (
  SELECT substring(lower(v) FROM p FOR n) AS token
  FROM catalog.search_values(value) v
  CROSS JOIN generate_series(1,2) n
  CROSS JOIN LATERAL generate_series(1,length(v)-n+1) p
 ) tokens
$$;
CREATE INDEX IF NOT EXISTS catalog_course_search_v2 ON catalog.objects USING gin(catalog.search_tokens_v2(data->'object')) WHERE kind='course';
CREATE INDEX IF NOT EXISTS catalog_teacher_search_v2 ON catalog.objects USING gin(catalog.search_tokens_v2(data->'object')) WHERE kind='teacher';
