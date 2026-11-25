-- The school adapter has already resolved source-specific codes to Catalog codes.
-- Compare numeric strings by significant length, then digits, without integer overflow.
CREATE OR REPLACE FUNCTION catalog.department_order_key(code text, name text, id text)
RETURNS text[] LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog AS $$
 WITH normalized AS (SELECT btrim(COALESCE(code,'')) AS value),
 parts AS (SELECT value, CASE WHEN value ~ '^[0-9]+$' THEN '0' WHEN value ~ '^B[0-9]+$' THEN '1'
                             WHEN value<>'' THEN '2' ELSE '3' END AS class,
                 ltrim(regexp_replace(value,'^B',''),'0') AS digits FROM normalized)
 SELECT CASE WHEN class IN ('0','1') THEN ARRAY[class,lpad(length(digits)::text,10,'0'),digits,name,id]
             ELSE ARRAY[class,value,name,id] END FROM parts
$$;

ALTER TABLE catalog.objects ADD COLUMN IF NOT EXISTS department_sort_key text[] COLLATE "C";
CREATE OR REPLACE FUNCTION catalog.set_department_sort_key() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 NEW.department_sort_key := catalog.department_order_key(NEW.data->'object'->>'code',NEW.name,NEW.id);
 RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER catalog_department_sort_key BEFORE INSERT OR UPDATE OF data,name,id,kind ON catalog.objects
 FOR EACH ROW WHEN (NEW.kind='department') EXECUTE FUNCTION catalog.set_department_sort_key();
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM catalog.objects WHERE kind='department' AND department_sort_key IS NULL) THEN
  UPDATE catalog.objects SET department_sort_key=catalog.department_order_key(data->'object'->>'code',name,id)
  WHERE kind='department' AND department_sort_key IS NULL;
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS catalog_departments_in_order ON catalog.objects(school_id,department_sort_key) WHERE kind='department';

ALTER TABLE catalog.projection_objects ADD COLUMN IF NOT EXISTS department_sort_order integer;
CREATE INDEX IF NOT EXISTS projection_departments_in_order ON catalog.projection_objects(version,department_sort_order) WHERE kind='department';
CREATE OR REPLACE FUNCTION catalog.rank_projection_departments(generation bigint) RETURNS void
LANGUAGE sql SET search_path=pg_catalog AS $$
 WITH ranked AS (
  SELECT id,row_number() OVER (ORDER BY catalog.department_order_key(data->>'code',name,id) COLLATE "C")::integer AS position
  FROM catalog.projection_objects WHERE version=generation AND kind='department'
 )
 UPDATE catalog.projection_objects o SET department_sort_order=r.position FROM ranked r
 WHERE o.version=generation AND o.id=r.id AND o.department_sort_order IS DISTINCT FROM r.position
$$;

-- Existing default pages contain an ordered JSON array. Reorder just this array
-- in the same transaction as its generation's ranks, retaining all facet counts.
DO $$ DECLARE gen bigint;
BEGIN
 FOR gen IN SELECT DISTINCT version FROM catalog.projection_objects WHERE kind='department' AND department_sort_order IS NULL LOOP
  PERFORM catalog.rank_projection_departments(gen);
  UPDATE catalog.projection_selections s SET data=jsonb_set(s.data,'{department_facets}',
   COALESCE((SELECT jsonb_agg(f.item ORDER BY d.department_sort_order)
    FROM jsonb_array_elements(s.data->'department_facets') f(item)
    JOIN catalog.projection_objects d ON d.version=s.version AND d.id=f.item->>'id' AND d.kind='department'),'[]'::jsonb))
  WHERE s.version=gen;
 END LOOP;
END $$;
