CREATE TABLE IF NOT EXISTS catalog.teacher_homepage_requests (
 id text COLLATE "C" PRIMARY KEY,
 school_id text NOT NULL REFERENCES catalog.objects(id),
 teacher_id text NOT NULL REFERENCES catalog.objects(id),
 submitter text NOT NULL CHECK(submitter<>''),
 status text NOT NULL CHECK(status IN ('pending','approved','rejected')),
 revision bigint NOT NULL CHECK(revision>0),
 created_at timestamptz NOT NULL,
 resolved_at timestamptz,
 retention_due_at timestamp GENERATED ALWAYS AS ((resolved_at AT TIME ZONE 'UTC') + interval '12 months') STORED,
 data jsonb NOT NULL CHECK(jsonb_typeof(data)='object'),
 data_bytes integer GENERATED ALWAYS AS (octet_length(data::text)) STORED,
 CHECK(NOT COALESCE((data #> '{value,photo}') ? 'data',false)),
 CHECK((status='pending' AND revision=1 AND resolved_at IS NULL)
       OR (status<>'pending' AND revision>1 AND resolved_at IS NOT NULL AND resolved_at>=created_at))
);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_queue
 ON catalog.teacher_homepage_requests(school_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_queue_status
 ON catalog.teacher_homepage_requests(school_id,status,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_owner
 ON catalog.teacher_homepage_requests(school_id,teacher_id,submitter,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_rate
 ON catalog.teacher_homepage_requests(submitter,created_at DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_expiry
 ON catalog.teacher_homepage_requests(retention_due_at,id) WHERE retention_due_at IS NOT NULL;

-- Governance records use the same request scope and are only visible to the
-- approved migration/governance identity. These fields are never API data.
CREATE TABLE IF NOT EXISTS catalog.teacher_homepage_request_holds (
 request_id text COLLATE "C" PRIMARY KEY REFERENCES catalog.teacher_homepage_requests(id) ON DELETE CASCADE,
 reference text NOT NULL CHECK(char_length(btrim(reference)) BETWEEN 1 AND 160),
 basis text NOT NULL CHECK(char_length(btrim(basis)) BETWEEN 1 AND 2000),
 purpose text NOT NULL CHECK(char_length(btrim(purpose)) BETWEEN 1 AND 2000),
 end_condition text NOT NULL CHECK(char_length(btrim(end_condition)) BETWEEN 1 AND 2000),
 registered_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 review_at timestamptz NOT NULL,
 retain_until timestamptz NOT NULL,
 released_at timestamptz,
 CHECK(registered_at<=review_at AND review_at<=retain_until AND registered_at<retain_until),
 CHECK(released_at IS NULL OR released_at>=registered_at)
);

CREATE TABLE IF NOT EXISTS catalog.teacher_homepage_request_photos (
 request_id text COLLATE "C" PRIMARY KEY REFERENCES catalog.teacher_homepage_requests(id) ON DELETE CASCADE,
 photo_id text NOT NULL,
 media_type text NOT NULL CHECK(media_type IN ('image/jpeg','image/png')),
 width integer NOT NULL CHECK(width BETWEEN 1 AND 4096),
 height integer NOT NULL CHECK(height BETWEEN 1 AND 4096),
 data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 2097152),
 CHECK(width::bigint * height <= 8388608),
 CHECK(photo_id = encode(sha256(data),'hex'))
);

CREATE TABLE IF NOT EXISTS catalog.teacher_homepage_request_replays (
 key text PRIMARY KEY REFERENCES catalog.replays(key) ON DELETE CASCADE
  CHECK(key ~ '^teacher-homepage-request-(submit|resolve):[0-9a-f]{64}$'),
 request_id text COLLATE "C" NOT NULL REFERENCES catalog.teacher_homepage_requests(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_request_replay_target
 ON catalog.teacher_homepage_request_replays(request_id);

REVOKE ALL ON TABLE catalog.teacher_homepage_requests,catalog.teacher_homepage_request_photos,
 catalog.teacher_homepage_request_replays,catalog.teacher_homepage_request_holds FROM PUBLIC;

CREATE INDEX IF NOT EXISTS catalog_teacher_homepage_audit_expiry
 ON catalog.audit (((audit_time AT TIME ZONE 'UTC') + interval '12 months'),id COLLATE "C")
 WHERE audit_action LIKE 'catalog.teacher-homepage-request.%'
  OR audit_action LIKE 'catalog.teacher-sites.homepage-%'
  OR audit_action IN ('catalog.teacher-sites.request-resolved.v1','catalog.teacher-sites.requests-viewed.v1',
                      'catalog.teacher-sites.request-viewed.v1','catalog.teacher-sites.request-photo-viewed.v1');

-- A hold update and the audit sweep must use the same order even when the
-- requester row is not itself due for deletion.
CREATE OR REPLACE FUNCTION catalog.lock_teacher_homepage_legal_hold() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('catalog:teacher-homepage-legal-hold',0));
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION catalog.lock_teacher_homepage_legal_hold() FROM PUBLIC;
CREATE OR REPLACE TRIGGER lock_teacher_homepage_legal_hold BEFORE INSERT OR UPDATE OR DELETE
 ON catalog.teacher_homepage_request_holds FOR EACH STATEMENT EXECUTE FUNCTION catalog.lock_teacher_homepage_legal_hold();

-- Run by the existing approved Catalog governance identity. Keep the hold
-- write and its audit record inseparable, even for an extension or release.
CREATE OR REPLACE FUNCTION catalog.register_teacher_homepage_request_hold(
 p_request_id text,p_reference text,p_basis text,p_purpose text,p_end_condition text,
 p_review_at timestamptz,p_retain_until timestamptz,p_actor text
) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
DECLARE changed_at timestamptz := clock_timestamp();
BEGIN
 IF COALESCE(char_length(btrim(p_actor)),0) NOT BETWEEN 1 AND 256 THEN
  RAISE EXCEPTION 'invalid governance actor' USING ERRCODE='22023';
 END IF;
 INSERT INTO catalog.teacher_homepage_request_holds(
  request_id,reference,basis,purpose,end_condition,registered_at,review_at,retain_until)
 VALUES(p_request_id,p_reference,p_basis,p_purpose,p_end_condition,changed_at,p_review_at,p_retain_until)
 ON CONFLICT(request_id) DO UPDATE SET reference=excluded.reference,basis=excluded.basis,purpose=excluded.purpose,
  end_condition=excluded.end_condition,registered_at=excluded.registered_at,review_at=excluded.review_at,
  retain_until=excluded.retain_until,released_at=NULL;
 INSERT INTO catalog.audit(id,result,audit_actor,audit_target,audit_action,audit_school,audit_time,data)
 SELECT gen_random_uuid()::text,'success',p_actor,'teacher-homepage-request/'||r.id,
  'catalog.teacher-homepage-request.hold-registered.v1',r.school_id,changed_at,jsonb_build_object('reference',p_reference)
 FROM catalog.teacher_homepage_requests r WHERE r.id=p_request_id;
END $$;
REVOKE ALL ON FUNCTION catalog.register_teacher_homepage_request_hold(text,text,text,text,text,timestamptz,timestamptz,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION catalog.release_teacher_homepage_request_hold(p_request_id text,p_actor text)
RETURNS boolean LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
DECLARE
 changed_at timestamptz := clock_timestamp();
 hold_reference text;
BEGIN
 IF COALESCE(char_length(btrim(p_actor)),0) NOT BETWEEN 1 AND 256 THEN
  RAISE EXCEPTION 'invalid governance actor' USING ERRCODE='22023';
 END IF;
 UPDATE catalog.teacher_homepage_request_holds SET released_at=changed_at
  WHERE request_id=p_request_id AND released_at IS NULL RETURNING reference INTO hold_reference;
 IF NOT FOUND THEN
  RETURN false;
 END IF;
 INSERT INTO catalog.audit(id,result,audit_actor,audit_target,audit_action,audit_school,audit_time,data)
 SELECT gen_random_uuid()::text,'success',p_actor,'teacher-homepage-request/'||r.id,
  'catalog.teacher-homepage-request.hold-released.v1',r.school_id,changed_at,jsonb_build_object('reference',hold_reference)
 FROM catalog.teacher_homepage_requests r WHERE r.id=p_request_id;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION catalog.release_teacher_homepage_request_hold(text,text) FROM PUBLIC;

-- The maintenance login can call this specific operation without obtaining
-- access to private request data, direct DELETE, or authority over legal holds.
CREATE OR REPLACE FUNCTION catalog.cleanup_teacher_homepage_requests() RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE
 cutoff timestamp := clock_timestamp() AT TIME ZONE 'UTC';
 request_ids text[];
 audit_ids text[];
 removed_requests bigint := 0;
 removed_audit bigint := 0;
BEGIN
 IF NOT pg_try_advisory_xact_lock(hashtextextended('catalog:teacher-homepage-legal-hold',0)) THEN
  RETURN 0;
 END IF;
 SELECT ARRAY(
  SELECT r.id FROM catalog.teacher_homepage_requests r
  WHERE r.retention_due_at<=cutoff AND NOT EXISTS (
   SELECT 1 FROM catalog.teacher_homepage_request_holds h WHERE h.request_id=r.id
   AND h.released_at IS NULL AND (h.retain_until AT TIME ZONE 'UTC')>cutoff)
  ORDER BY r.retention_due_at,r.id LIMIT 1000 FOR UPDATE SKIP LOCKED
 ) INTO request_ids;
 IF cardinality(request_ids)>0 THEN
  DELETE FROM catalog.replays p USING catalog.teacher_homepage_request_replays l
   WHERE p.key=l.key AND l.request_id=ANY(request_ids);
  DELETE FROM catalog.teacher_homepage_requests WHERE id=ANY(request_ids);
  GET DIAGNOSTICS removed_requests=ROW_COUNT;
 END IF;

 SELECT ARRAY(
  SELECT a.id FROM catalog.audit a
  WHERE (a.audit_action LIKE 'catalog.teacher-homepage-request.%'
   OR a.audit_action LIKE 'catalog.teacher-sites.homepage-%'
   OR a.audit_action IN ('catalog.teacher-sites.request-resolved.v1','catalog.teacher-sites.requests-viewed.v1',
                         'catalog.teacher-sites.request-viewed.v1','catalog.teacher-sites.request-photo-viewed.v1'))
  AND (a.audit_time AT TIME ZONE 'UTC') + interval '12 months'<=cutoff
  AND NOT EXISTS (SELECT 1 FROM catalog.teacher_homepage_request_holds h
   WHERE a.audit_target LIKE 'teacher-homepage-request/%'
    AND h.request_id=substr(a.audit_target,length('teacher-homepage-request/')+1)
    AND h.released_at IS NULL AND (h.retain_until AT TIME ZONE 'UTC')>cutoff)
  ORDER BY (a.audit_time AT TIME ZONE 'UTC') + interval '12 months',a.id COLLATE "C"
  LIMIT (1000-removed_requests) FOR UPDATE OF a SKIP LOCKED
 ) INTO audit_ids;
 IF cardinality(audit_ids)>0 THEN
  DELETE FROM catalog.audit WHERE id=ANY(audit_ids);
  GET DIAGNOSTICS removed_audit=ROW_COUNT;
 END IF;
 RETURN removed_requests+removed_audit;
END $$;
REVOKE ALL ON FUNCTION catalog.cleanup_teacher_homepage_requests() FROM PUBLIC;
