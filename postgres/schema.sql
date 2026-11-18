CREATE SCHEMA IF NOT EXISTS catalog;
CREATE TABLE IF NOT EXISTS catalog.recent_courses (
 owner text NOT NULL, course_id text NOT NULL, visited_at timestamptz NOT NULL,
 PRIMARY KEY(owner,course_id)
);
CREATE INDEX IF NOT EXISTS catalog_recent_courses_owner ON catalog.recent_courses(owner,visited_at DESC,course_id);
CREATE INDEX IF NOT EXISTS catalog_recent_courses_expiry ON catalog.recent_courses(visited_at,owner,course_id);
REVOKE ALL ON TABLE catalog.recent_courses FROM PUBLIC;
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
-- Persistent directory evidence is independent of teacher source records. A
-- failed website refresh cannot remove teaching facts or the last complete set.
CREATE TABLE IF NOT EXISTS catalog.teacher_directory (
 teacher_id text PRIMARY KEY REFERENCES catalog.objects(id), school_id text NOT NULL,
 teaching boolean NOT NULL DEFAULT false, official boolean NOT NULL DEFAULT false,
 matched_name text NOT NULL DEFAULT '', matched_department_id text NOT NULL DEFAULT '', homepage_url text NOT NULL DEFAULT '',
 teaching_updated_at timestamptz, official_updated_at timestamptz, updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_teacher_directory_school ON catalog.teacher_directory(school_id,teacher_id) WHERE teaching OR official;
CREATE TABLE IF NOT EXISTS catalog.teacher_site_sources (
 school_id text PRIMARY KEY REFERENCES catalog.objects(id), source_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), digest text NOT NULL, observed_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog.history(id text NOT NULL, revision bigint NOT NULL, data jsonb NOT NULL, PRIMARY KEY(id,revision));
CREATE TABLE IF NOT EXISTS catalog.enrollments (
 id text PRIMARY KEY, subject text NOT NULL, school_id text NOT NULL, course_id text NOT NULL,
 term_id text NOT NULL, active boolean NOT NULL, data jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_enrollment_owner ON catalog.enrollments(subject,school_id,course_id,id) WHERE active;
CREATE TABLE IF NOT EXISTS catalog.replays(key text PRIMARY KEY, data jsonb NOT NULL, expires_at timestamptz);
ALTER TABLE catalog.replays ADD COLUMN IF NOT EXISTS expires_at timestamptz;
CREATE INDEX IF NOT EXISTS catalog_replays_expiry ON catalog.replays(expires_at,key) WHERE expires_at IS NOT NULL;
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
CREATE INDEX IF NOT EXISTS catalog_outbox_sent ON catalog.outbox(sent_at,id) WHERE sent_at IS NOT NULL;

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
ALTER TABLE catalog.view_retention ADD COLUMN IF NOT EXISTS legacy_until_day date NOT NULL DEFAULT ((CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date);
REVOKE ALL ON catalog.view_retention FROM PUBLIC;
CREATE TABLE IF NOT EXISTS catalog.view_day_keys(day date PRIMARY KEY,key bytea NOT NULL CHECK(octet_length(key)=32));
REVOKE ALL ON catalog.view_day_keys FROM PUBLIC;

-- Classification candidates include inherited offering/class values. Keep the
-- selector before school so trusted internal cross-school reads also use it.
CREATE INDEX IF NOT EXISTS catalog_department_filter ON catalog.objects(kind,department_id,school_id,id) INCLUDE(course_id,term_course_id) WHERE department_id<>'';
CREATE INDEX IF NOT EXISTS catalog_category_filter ON catalog.objects(kind,category_id,school_id,id) INCLUDE(course_id,term_course_id) WHERE category_id<>'';
CREATE INDEX IF NOT EXISTS catalog_nature_filter ON catalog.objects(kind,nature_id,school_id,id) INCLUDE(course_id,term_course_id) WHERE nature_id<>'';

-- Chinese names commonly use one/two-character searches, for which pg_trgm
-- cannot extract a trigram. Built-in GIN array_ops supports those searches
-- without extension privileges. Tokens are only a candidate filter; List
-- rechecks the literal substring independently in name, code and each alias.
CREATE OR REPLACE FUNCTION catalog.search_tokens(value jsonb) RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT COALESCE(array_agg(DISTINCT token),'{}'::text[]) FROM (
  SELECT substring(v FROM p FOR n) AS token
  FROM (
   SELECT lower(COALESCE(value->>'name','')) AS v
   UNION ALL SELECT lower(COALESCE(value->>'code',''))
   UNION ALL SELECT lower(a) FROM jsonb_array_elements_text(COALESCE(NULLIF(value->'aliases','null'::jsonb),'[]'::jsonb)) a
  ) values_to_index
  CROSS JOIN generate_series(1,2) n
  CROSS JOIN LATERAL generate_series(1,length(v)-n+1) p
 ) tokens
$$;
CREATE INDEX IF NOT EXISTS catalog_name_tokens ON catalog.objects USING gin(catalog.search_tokens(data->'object')) WHERE kind IN ('course','teacher');
-- Keep each directory's posting lists separate: a teacher-name query must not
-- fetch thousands of course rows that share the same short Chinese token.
CREATE INDEX IF NOT EXISTS catalog_course_name_tokens ON catalog.objects USING gin(catalog.search_tokens(data->'object')) WHERE kind='course';
CREATE INDEX IF NOT EXISTS catalog_teacher_name_tokens ON catalog.objects USING gin(catalog.search_tokens(data->'object')) WHERE kind='teacher';

-- Cover the class-to-offering candidate join without reading JSON heap rows.
CREATE INDEX IF NOT EXISTS catalog_offering_course_lookup ON catalog.objects(id) INCLUDE(course_id) WHERE kind='term_course';
CREATE INDEX IF NOT EXISTS catalog_class_offering_lookup ON catalog.objects(id) INCLUDE(term_course_id,department_id,category_id,nature_id) WHERE kind='class';
CREATE INDEX IF NOT EXISTS catalog_class_directory_lookup ON catalog.objects(term_course_id) INCLUDE(department_id,category_id,nature_id) WHERE kind='class';
CREATE INDEX IF NOT EXISTS catalog_offering_directory_lookup ON catalog.objects(course_id) INCLUDE(id,department_id,category_id,nature_id) WHERE kind='term_course';

-- Facets scan bounded classification columns, not the JSON payload of every
-- class/offering. Existing list/search indexes remain unchanged.
CREATE INDEX IF NOT EXISTS catalog_active_directory ON catalog.objects(kind,school_id,id) INCLUDE(name,department_id,category_id,nature_id)
 WHERE kind IN ('course','teacher') AND NOT COALESCE((data->'object'->>'inactive')::boolean,false);
CREATE INDEX IF NOT EXISTS catalog_directory_offering_facets ON catalog.objects(school_id,course_id,id) INCLUDE(department_id,category_id,nature_id)
 WHERE kind='term_course' AND NOT COALESCE((data->'object'->>'inactive')::boolean,false);
CREATE INDEX IF NOT EXISTS catalog_directory_class_facets ON catalog.objects(school_id,term_course_id,id) INCLUDE(department_id,category_id,nature_id)
 WHERE kind='class' AND NOT COALESCE((data->'object'->>'inactive')::boolean,false);
CREATE INDEX IF NOT EXISTS catalog_activity_window ON catalog.activity_buckets(school_id,kind,day,target_id) INCLUDE(first_count) WHERE visible;

-- Changes invalidate cached membership in the same transaction as the object,
-- alias or T2 visibility update. Runtime code only reads this generation.
CREATE TABLE IF NOT EXISTS catalog.directory_revision(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),revision bigint NOT NULL DEFAULT 1);
INSERT INTO catalog.directory_revision(singleton) VALUES(true) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION catalog.invalidate_directory() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN UPDATE catalog.directory_revision SET revision=revision+1 WHERE singleton; RETURN NULL; END $$;
REVOKE ALL ON FUNCTION catalog.invalidate_directory() FROM PUBLIC;
CREATE OR REPLACE TRIGGER invalidate_directory AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON catalog.objects FOR EACH STATEMENT EXECUTE FUNCTION catalog.invalidate_directory();
CREATE OR REPLACE TRIGGER invalidate_directory AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON catalog.redirects FOR EACH STATEMENT EXECUTE FUNCTION catalog.invalidate_directory();
CREATE OR REPLACE TRIGGER invalidate_directory AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON catalog.teacher_directory FOR EACH STATEMENT EXECUTE FUNCTION catalog.invalidate_directory();
REVOKE ALL ON TABLE catalog.directory_revision FROM PUBLIC;

-- Teacher refresh control and sanitized review data belong to catalog. One
-- active job across all schools fences timers, manual requests and workers.
CREATE TABLE IF NOT EXISTS catalog.teacher_refresh_runs (
 id text PRIMARY KEY, school_id text NOT NULL, status text NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out')),
 requested_at timestamptz NOT NULL, data jsonb NOT NULL
);
ALTER TABLE catalog.teacher_refresh_runs ADD COLUMN IF NOT EXISTS position bigint GENERATED ALWAYS AS IDENTITY;
CREATE UNIQUE INDEX IF NOT EXISTS catalog_teacher_refresh_active ON catalog.teacher_refresh_runs((true)) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS catalog_teacher_refresh_latest ON catalog.teacher_refresh_runs(school_id,requested_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_refresh_position ON catalog.teacher_refresh_runs(school_id,position DESC);
CREATE INDEX IF NOT EXISTS catalog_teacher_refresh_prune ON catalog.teacher_refresh_runs(requested_at,id) WHERE status IN ('succeeded','failed','timed_out');
CREATE TABLE IF NOT EXISTS catalog.teacher_unmatched_snapshots (
 school_id text PRIMARY KEY, revision bigint NOT NULL, observed_at timestamptz NOT NULL, digest text NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog.teacher_unmatched_sites (
 school_id text NOT NULL, site_id text NOT NULL, reason text NOT NULL, data jsonb NOT NULL,
 PRIMARY KEY(school_id,site_id)
);
CREATE INDEX IF NOT EXISTS catalog_teacher_unmatched_reason ON catalog.teacher_unmatched_sites(school_id,reason,site_id);
-- Versioned public directory. Source objects and private enrollments remain owned
-- by their existing tables. A head is published only after every projection passes.
CREATE TABLE IF NOT EXISTS catalog.term_orders (
 school_id text NOT NULL, id text PRIMARY KEY, sort_key integer NOT NULL,
 data jsonb NOT NULL, CHECK(sort_key=0 OR sort_key BETWEEN 19001 AND 22003)
);
CREATE TABLE IF NOT EXISTS catalog.projection_generations (
 version bigint PRIMARY KEY, school_id text NOT NULL, policy jsonb NOT NULL,
 source_revision bigint NOT NULL, built_at timestamptz NOT NULL, report jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS catalog.projection_heads (
 school_id text PRIMARY KEY, version bigint NOT NULL, policy jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog.projection_failures (
 school_id text PRIMARY KEY, failed_at timestamptz NOT NULL, reason text NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog.projection_imports (
 school_id text PRIMARY KEY, batch text NOT NULL, state text NOT NULL CHECK(state IN('running','failed','complete')),
 started_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, deadline timestamptz NOT NULL
);
ALTER TABLE catalog.projection_imports ADD COLUMN IF NOT EXISTS attempt_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS catalog.projection_objects (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 id text NOT NULL, school_id text NOT NULL, kind text NOT NULL, name text NOT NULL,
 department_id text NOT NULL DEFAULT '', category_id text NOT NULL DEFAULT '', nature_id text NOT NULL DEFAULT '',
 course_id text NOT NULL DEFAULT '', term_id text NOT NULL DEFAULT '', term_course_id text NOT NULL DEFAULT '',
 class_id text NOT NULL DEFAULT '', teacher_id text NOT NULL DEFAULT '',
 term_key integer NOT NULL DEFAULT 0, visible boolean NOT NULL DEFAULT false,
 teaching boolean NOT NULL DEFAULT false, official boolean NOT NULL DEFAULT false, data jsonb NOT NULL,
 PRIMARY KEY(version,id)
);
CREATE INDEX IF NOT EXISTS projection_objects_page ON catalog.projection_objects(version,kind,id) WHERE visible;
CREATE INDEX IF NOT EXISTS projection_objects_terms ON catalog.projection_objects(version,kind,term_key DESC,id) WHERE visible;
CREATE INDEX IF NOT EXISTS projection_objects_course ON catalog.projection_objects(version,course_id,kind,id);
CREATE INDEX IF NOT EXISTS projection_objects_class ON catalog.projection_objects(version,class_id,kind,id);
CREATE INDEX IF NOT EXISTS projection_objects_offering ON catalog.projection_objects(version,term_course_id,kind,id);
CREATE INDEX IF NOT EXISTS projection_objects_teacher ON catalog.projection_objects(version,teacher_id,kind,id);
-- Source-to-project membership is versioned with the public directory. Unknown
-- classes retain a reason for aggregate diagnostics and never receive a target.
CREATE TABLE IF NOT EXISTS catalog.projection_sports_sources (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 course_id text NOT NULL, PRIMARY KEY(version,course_id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_sports_classes (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 class_id text NOT NULL, source_course_id text NOT NULL, term_id text NOT NULL,
 project_id text NOT NULL, offering_id text NOT NULL, reason text NOT NULL,
 PRIMARY KEY(version,class_id), CHECK(reason IN('','missing_project_code','unknown_project_code','missing_teacher'))
);
CREATE INDEX IF NOT EXISTS projection_sports_project ON catalog.projection_sports_classes(version,project_id,class_id) WHERE reason='';
REVOKE ALL ON catalog.projection_sports_sources,catalog.projection_sports_classes FROM PUBLIC;
CREATE TABLE IF NOT EXISTS catalog.projection_variants (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 course_id text NOT NULL, offering_id text NOT NULL, class_id text NOT NULL, term_id text NOT NULL,
 term_key integer NOT NULL, department_id text NOT NULL, category_id text NOT NULL, nature_id text NOT NULL,
 teacher_ids text[] NOT NULL DEFAULT '{}', PRIMARY KEY(version,course_id,offering_id,class_id)
);
CREATE INDEX IF NOT EXISTS projection_variants_department ON catalog.projection_variants(version,department_id,course_id);
CREATE INDEX IF NOT EXISTS projection_variants_category ON catalog.projection_variants(version,category_id,course_id);
CREATE INDEX IF NOT EXISTS projection_variants_nature ON catalog.projection_variants(version,nature_id,course_id);
CREATE INDEX IF NOT EXISTS projection_variants_teachers ON catalog.projection_variants USING gin(teacher_ids);
CREATE TABLE IF NOT EXISTS catalog.projection_relations (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 course_id text NOT NULL, teacher_id text NOT NULL, term_id text NOT NULL, offering_id text NOT NULL,
 PRIMARY KEY(version,course_id,teacher_id,term_id,offering_id)
);
CREATE INDEX IF NOT EXISTS projection_relations_teacher ON catalog.projection_relations(version,teacher_id,course_id);
CREATE TABLE IF NOT EXISTS catalog.projection_teaching_history (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 teacher_id text NOT NULL,course_id text NOT NULL,PRIMARY KEY(version,teacher_id,course_id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_options (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 owner_kind text NOT NULL, kind text NOT NULL, id text NOT NULL, name text NOT NULL,
 count bigint NOT NULL CHECK(count>0), term_key integer NOT NULL DEFAULT 0,
 PRIMARY KEY(version,owner_kind,kind,id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_aliases (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 alias_id text NOT NULL, canonical_id text NOT NULL, PRIMARY KEY(version,alias_id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_summaries (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 id text NOT NULL, data jsonb NOT NULL, PRIMARY KEY(version,id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_documents (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 id text NOT NULL, data jsonb NOT NULL, PRIMARY KEY(version,id)
);
CREATE TABLE IF NOT EXISTS catalog.projection_selections (
 version bigint NOT NULL REFERENCES catalog.projection_generations(version) ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN('course','teacher')),data jsonb NOT NULL,
 PRIMARY KEY(version,kind),CHECK(jsonb_typeof(data)='object')
);
-- Fixed owner function for fresh-generation planner statistics. No source
-- tables or arbitrary SQL are accessible through this maintenance capability.
CREATE OR REPLACE FUNCTION catalog.analyze_directory_projection() RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN
 ANALYZE catalog.projection_objects;
 ANALYZE catalog.projection_sports_sources;
 ANALYZE catalog.projection_sports_classes;
 ANALYZE catalog.projection_variants;
 ANALYZE catalog.projection_relations;
 ANALYZE catalog.projection_options;
 ANALYZE catalog.projection_aliases;
 ANALYZE catalog.projection_heads;
 ANALYZE catalog.projection_generations;
 ANALYZE catalog.projection_summaries;
 ANALYZE catalog.projection_documents;
 ANALYZE catalog.projection_selections;
 ANALYZE catalog.projection_teaching_history;
END $$;
REVOKE ALL ON FUNCTION catalog.analyze_directory_projection() FROM PUBLIC;
