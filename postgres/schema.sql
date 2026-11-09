CREATE SCHEMA IF NOT EXISTS catalog;
CREATE TABLE IF NOT EXISTS catalog.objects (
 id text PRIMARY KEY, school_id text NOT NULL, kind text NOT NULL, name text NOT NULL,
 department_id text NOT NULL, category_id text NOT NULL, nature_id text NOT NULL,
 course_id text NOT NULL, term_id text NOT NULL, term_course_id text NOT NULL,
 class_id text NOT NULL, teacher_id text NOT NULL, data jsonb NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS catalog_term_course_unique ON catalog.objects(course_id,term_id) WHERE kind='term_course';
CREATE UNIQUE INDEX IF NOT EXISTS catalog_assignment_unique ON catalog.objects(class_id,teacher_id) WHERE kind='assignment';
CREATE INDEX IF NOT EXISTS catalog_directory ON catalog.objects(kind,school_id,id);
CREATE INDEX IF NOT EXISTS catalog_classes ON catalog.objects(term_course_id) WHERE kind='class';
CREATE INDEX IF NOT EXISTS catalog_teacher_classes ON catalog.objects(teacher_id,class_id) WHERE kind='assignment';
CREATE TABLE IF NOT EXISTS catalog.history(id text NOT NULL, revision bigint NOT NULL, data jsonb NOT NULL, PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS catalog.enrollments (
 id text PRIMARY KEY, subject text NOT NULL, school_id text NOT NULL, course_id text NOT NULL,
 term_id text NOT NULL, active boolean NOT NULL, data jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_enrollment_owner ON catalog.enrollments(subject,school_id,course_id,id) WHERE active;
CREATE TABLE IF NOT EXISTS catalog.replays(key text PRIMARY KEY, data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS catalog.audit(
 id text PRIMARY KEY, data jsonb NOT NULL,
 reason text NOT NULL DEFAULT '', result text NOT NULL DEFAULT 'unknown' CHECK(result IN ('success','denied','failed','unknown')),
 recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 audit_actor text NOT NULL DEFAULT 'unknown',audit_target text NOT NULL DEFAULT 'unknown',audit_action text NOT NULL DEFAULT 'unknown',
 audit_school text NOT NULL DEFAULT '*',audit_group text NOT NULL DEFAULT '*',audit_time timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS catalog.outbox(id text PRIMARY KEY, data jsonb NOT NULL, sent_at timestamptz);
REVOKE ALL ON SCHEMA catalog FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA catalog FROM PUBLIC;

CREATE TABLE IF NOT EXISTS catalog.redirects(alias_id text PRIMARY KEY REFERENCES catalog.objects(id),canonical_id text NOT NULL REFERENCES catalog.objects(id),reason text NOT NULL,created_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS catalog_redirect_targets ON catalog.redirects(canonical_id,alias_id);
CREATE OR REPLACE FUNCTION catalog.canonical(value text) RETURNS text LANGUAGE sql STABLE AS $$ SELECT COALESCE((SELECT canonical_id FROM catalog.redirects WHERE alias_id=value),value) $$;
CREATE OR REPLACE FUNCTION catalog.public_object(value jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT value || jsonb_build_object('course_id',catalog.canonical(COALESCE(value->>'course_id','')),'teacher_id',catalog.canonical(COALESCE(value->>'teacher_id','')),'term_course_id',catalog.canonical(COALESCE(value->>'term_course_id','')),'class_id',catalog.canonical(COALESCE(value->>'class_id','')))
$$;
REVOKE ALL ON TABLE catalog.redirects FROM PUBLIC;
-- Invoker rights preserve each caller's table permissions.

-- Name lookup also serves historical enrollment records after qualification expiry.
CREATE INDEX IF NOT EXISTS catalog_enrollment_history_owner ON catalog.enrollments(subject,id);

CREATE INDEX IF NOT EXISTS catalog_schedule_class ON catalog.objects(class_id,school_id,id) WHERE kind='schedule';

CREATE TABLE IF NOT EXISTS catalog.timetables(school_id text NOT NULL REFERENCES catalog.objects(id),term_id text NOT NULL REFERENCES catalog.objects(id),data jsonb NOT NULL,PRIMARY KEY(school_id,term_id));
REVOKE ALL ON TABLE catalog.timetables FROM PUBLIC;
CREATE INDEX IF NOT EXISTS catalog_outbox_pending ON catalog.outbox((data->>'source') COLLATE "C",(data->>'subject') COLLATE "C",((data->'data'->>'revision')::bigint),id COLLATE "C") WHERE sent_at IS NULL;

CREATE TABLE IF NOT EXISTS catalog.audit_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 0);
INSERT INTO catalog.audit_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION catalog.audit_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE catalog.audit_retention SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION catalog.audit_invalidate() FROM PUBLIC;
CREATE OR REPLACE TRIGGER audit_invalidate AFTER UPDATE OR DELETE OR TRUNCATE ON catalog.audit FOR EACH STATEMENT EXECUTE FUNCTION catalog.audit_invalidate();
CREATE INDEX IF NOT EXISTS catalog_audit_query ON catalog.audit(audit_time DESC,id COLLATE "C" DESC);
CREATE TABLE IF NOT EXISTS catalog.discovery_history(id text NOT NULL,revision bigint NOT NULL,data jsonb NOT NULL,recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS catalog.discovery_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 0);
INSERT INTO catalog.discovery_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION catalog.discovery_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE catalog.discovery_retention SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION catalog.discovery_invalidate() FROM PUBLIC;
CREATE OR REPLACE TRIGGER discovery_invalidate AFTER UPDATE OR DELETE OR TRUNCATE ON catalog.discovery_history FOR EACH STATEMENT EXECUTE FUNCTION catalog.discovery_invalidate();
REVOKE ALL ON TABLE catalog.audit_retention,catalog.discovery_history,catalog.discovery_retention FROM PUBLIC;
CREATE INDEX IF NOT EXISTS catalog_discovery_order ON catalog.discovery_history(id COLLATE "C",revision DESC);
CREATE INDEX IF NOT EXISTS catalog_audit_school_order ON catalog.audit(audit_school,audit_time DESC,id COLLATE "C" DESC);

-- Provider-owned daily aggregates and immutable versions used by rebuild cursors.
CREATE TABLE IF NOT EXISTS catalog.activity_buckets(id text PRIMARY KEY,kind text NOT NULL CHECK(kind IN('course','teacher')),target_id text NOT NULL,school_id text NOT NULL,day date NOT NULL,first_count bigint NOT NULL,second_count bigint NOT NULL,revision bigint NOT NULL CHECK(revision>0),visible boolean NOT NULL,data jsonb NOT NULL DEFAULT '{}',UNIQUE(kind,target_id,day));
CREATE INDEX IF NOT EXISTS catalog_activity_target ON catalog.activity_buckets(kind,target_id,day);
CREATE TABLE IF NOT EXISTS catalog.activity_history(id text NOT NULL,revision bigint NOT NULL,day date NOT NULL,data jsonb NOT NULL,recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),PRIMARY KEY(id,revision));
CREATE INDEX IF NOT EXISTS catalog_activity_history_page ON catalog.activity_history(id COLLATE "C",revision DESC);
CREATE INDEX IF NOT EXISTS catalog_activity_history_day ON catalog.activity_history(day,id,revision);
CREATE TABLE IF NOT EXISTS catalog.activity_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 0);
INSERT INTO catalog.activity_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION catalog.invalidate_activity() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN UPDATE catalog.activity_retention SET revision=revision+1 WHERE singleton;RETURN NULL;END $$;
REVOKE ALL ON FUNCTION catalog.invalidate_activity() FROM PUBLIC;
CREATE OR REPLACE TRIGGER invalidate_activity AFTER UPDATE OR DELETE ON catalog.activity_history FOR EACH ROW EXECUTE FUNCTION catalog.invalidate_activity();
CREATE OR REPLACE TRIGGER truncate_activity AFTER TRUNCATE ON catalog.activity_history FOR EACH STATEMENT EXECUTE FUNCTION catalog.invalidate_activity();
REVOKE ALL ON TABLE catalog.activity_buckets,catalog.activity_history,catalog.activity_retention FROM PUBLIC;

-- No account or object identifiers are retained in the deduplication table.
CREATE TABLE IF NOT EXISTS catalog.view_dedup(digest text NOT NULL CHECK(length(digest)=64),day date NOT NULL,PRIMARY KEY(digest,day));
CREATE INDEX IF NOT EXISTS catalog_view_dedup_expiry ON catalog.view_dedup(day,digest);
REVOKE ALL ON catalog.view_dedup FROM PUBLIC;

CREATE TABLE IF NOT EXISTS catalog.view_retention(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),before_day date NOT NULL DEFAULT '0001-01-01');
INSERT INTO catalog.view_retention(singleton) VALUES(true) ON CONFLICT DO NOTHING;
REVOKE ALL ON catalog.view_retention FROM PUBLIC;
