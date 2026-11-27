-- Reservations survive object removal, merges and projection retirement.
CREATE TABLE IF NOT EXISTS catalog.addresses (
 school_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('course','teacher')),
 address text NOT NULL, object_id text NOT NULL, base text NOT NULL, current boolean NOT NULL DEFAULT false,
 PRIMARY KEY(school_id,kind,address),
 CHECK(kind<>'teacher' OR address ~ '^[1-9][0-9]{4}$')
);
CREATE UNIQUE INDEX IF NOT EXISTS catalog_current_address ON catalog.addresses(object_id) WHERE current;
CREATE INDEX IF NOT EXISTS catalog_address_base ON catalog.addresses(object_id,base);
CREATE TABLE IF NOT EXISTS catalog.address_usage (
 school_id text NOT NULL, pool text NOT NULL, used integer NOT NULL, PRIMARY KEY(school_id,pool)
);
CREATE TABLE IF NOT EXISTS catalog.address_migration_receipt (
 version integer PRIMARY KEY, migrated_at timestamptz NOT NULL, teachers bigint NOT NULL, courses bigint NOT NULL
);

CREATE OR REPLACE FUNCTION catalog.assign_address(school text, typ text, obj text, preferred text) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE candidate text; reserved text; n integer; usage_count integer; capacity integer;
BEGIN
 SELECT address INTO reserved FROM catalog.addresses WHERE object_id=obj AND base=preferred ORDER BY current DESC,address LIMIT 1;
 IF reserved IS NULL THEN
  FOR n IN 1..128 LOOP
   IF preferred='' THEN
    IF typ='teacher' THEN
     candidate:=(10000+floor(random()*90000)::int)::text;
    ELSE
     candidate:='course-'||(100000+floor(random()*900000)::int)::text;
    END IF;
   ELSE
    candidate:=preferred || CASE WHEN n=1 THEN '' ELSE '-'||n::text END;
   END IF;
   INSERT INTO catalog.addresses(school_id,kind,address,object_id,base)
    VALUES(school,typ,candidate,obj,preferred) ON CONFLICT DO NOTHING RETURNING address INTO reserved;
   EXIT WHEN reserved IS NOT NULL;
  END LOOP;
  IF reserved IS NULL THEN RAISE EXCEPTION 'catalog address allocation exhausted' USING ERRCODE='53000'; END IF;
  IF preferred='' THEN
   INSERT INTO catalog.address_usage VALUES(school,typ,1) ON CONFLICT(school_id,pool)
    DO UPDATE SET used=catalog.address_usage.used+1 RETURNING used INTO usage_count;
   capacity:=CASE WHEN typ='teacher' THEN 90000 ELSE 900000 END;
   IF usage_count=capacity*8/10 THEN
    RAISE WARNING 'catalog address pool reached 80 percent: pool=%, used=%, capacity=%',typ,usage_count,capacity;
   END IF;
  END IF;
 END IF;
 UPDATE catalog.addresses SET current=false WHERE object_id=obj AND current AND address<>reserved;
 UPDATE catalog.addresses SET current=true WHERE object_id=obj AND address=reserved AND NOT current;
END $$;
REVOKE ALL ON FUNCTION catalog.assign_address(text,text,text,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION catalog.course_address_base(code text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN code COLLATE "C" ~ '^[A-Za-z0-9._~-]{1,128}$' AND code NOT IN ('.','..') THEN code ELSE '' END
$$;

CREATE OR REPLACE FUNCTION catalog.reserve_source_address(school text, typ text, obj text, data jsonb) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE old_code text;
BEGIN
 PERFORM catalog.assign_address(school,typ,obj,CASE WHEN typ='course' THEN catalog.course_address_base(data->>'code') ELSE '' END);
 IF typ='course' THEN
  FOR old_code IN SELECT catalog.course_address_base(value) FROM jsonb_array_elements_text(COALESCE(NULLIF(data->'former_codes','null'),'[]')) LOOP
   IF old_code<>'' THEN
    INSERT INTO catalog.addresses(school_id,kind,address,object_id,base) VALUES(school,typ,old_code,obj,old_code)
     ON CONFLICT DO NOTHING;
   END IF;
  END LOOP;
 END IF;
END $$;
REVOKE ALL ON FUNCTION catalog.reserve_source_address(text,text,text,jsonb) FROM PUBLIC;

CREATE OR REPLACE FUNCTION catalog.sync_source_address() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.data->'object'->'code' IS NOT DISTINCT FROM NEW.data->'object'->'code'
  AND OLD.data->'object'->'former_codes' IS NOT DISTINCT FROM NEW.data->'object'->'former_codes' THEN RETURN NULL; END IF;
 PERFORM catalog.reserve_source_address(NEW.school_id,NEW.kind,NEW.id,NEW.data->'object');
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION catalog.sync_source_address() FROM PUBLIC;
CREATE OR REPLACE TRIGGER reserve_public_address AFTER INSERT OR UPDATE ON catalog.objects
 FOR EACH ROW WHEN (NEW.kind IN ('course','teacher')) EXECUTE FUNCTION catalog.sync_source_address();

-- Project objects live only in directory generations. Derive the address from
-- the same policy that produced the object's stable ID, including old heads.
CREATE OR REPLACE FUNCTION catalog.reserve_sport_address(ver bigint,school text,obj text) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE project_key text;
BEGIN
 SELECT p->>'key' INTO STRICT project_key FROM catalog.projection_generations g,
 LATERAL jsonb_array_elements(g.policy->'sports'->'projects') p WHERE g.version=ver
 AND obj='sports:course:'||substr(encode(sha256(convert_to(school,'UTF8')||decode('00','hex')||convert_to(p->>'key','UTF8')),'hex'),1,32);
 PERFORM catalog.assign_address(school,'course',obj,CASE WHEN project_key COLLATE "C" ~ '^[A-Za-z0-9._~-]{1,32}$' THEN 'sport-'||project_key ELSE '' END);
END $$;
REVOKE ALL ON FUNCTION catalog.reserve_sport_address(bigint,text,text) FROM PUBLIC;
CREATE OR REPLACE FUNCTION catalog.sync_sport_address() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 PERFORM catalog.reserve_sport_address(NEW.version,NEW.school_id,NEW.id);
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION catalog.sync_sport_address() FROM PUBLIC;
CREATE OR REPLACE TRIGGER reserve_sport_address AFTER INSERT ON catalog.projection_objects
 FOR EACH ROW WHEN (NEW.kind='course' AND NEW.data->>'course_type'='sports_project') EXECUTE FUNCTION catalog.sync_sport_address();

DO $$ DECLARE r record;
BEGIN
 IF EXISTS(SELECT FROM catalog.address_migration_receipt WHERE version=1) THEN RETURN; END IF;
 FOR r IN SELECT * FROM catalog.objects WHERE kind IN ('course','teacher')
  ORDER BY EXISTS(SELECT FROM catalog.redirects WHERE alias_id=id),id COLLATE "C" LOOP
  PERFORM catalog.reserve_source_address(r.school_id,r.kind,r.id,r.data->'object');
 END LOOP;
 FOR r IN SELECT DISTINCT ON(o.id) o.* FROM catalog.projection_objects o
  WHERE o.kind='course' AND o.data->>'course_type'='sports_project' ORDER BY o.id,o.version DESC LOOP
  PERFORM catalog.reserve_sport_address(r.version,r.school_id,r.id);
 END LOOP;
 INSERT INTO catalog.address_migration_receipt SELECT 1,clock_timestamp(),count(*) FILTER(WHERE kind='teacher'),count(*) FILTER(WHERE kind='course') FROM catalog.addresses WHERE current;
END $$;

CREATE OR REPLACE FUNCTION catalog.address_field(obj text) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT COALESCE((SELECT jsonb_build_object('address_id',address) FROM catalog.addresses WHERE object_id=obj AND current),'{}'::jsonb)
$$;

CREATE OR REPLACE FUNCTION catalog.address_refs(refs jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT COALESCE(jsonb_agg(r.value || catalog.address_field(r.value->>'id') ORDER BY r.ord),'[]'::jsonb)
 FROM jsonb_array_elements(refs) WITH ORDINALITY AS r(value,ord)
$$;
