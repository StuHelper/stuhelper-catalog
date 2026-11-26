-- A NULL checkpoint denotes a completed generation. Old readers still resolve
-- every object through the published head, never through the newest version.
ALTER TABLE catalog.projection_generations
 ADD COLUMN IF NOT EXISTS format_version integer NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS build_step integer,
 ADD COLUMN IF NOT EXISTS build_base bigint;
CREATE UNIQUE INDEX IF NOT EXISTS catalog_one_projection_draft
 ON catalog.projection_generations(school_id) WHERE build_step IS NOT NULL;

-- Term order is part of the source snapshot. Idempotent writes do not invalidate
-- a checkpoint unless a stored row actually changes.
CREATE OR REPLACE TRIGGER invalidate_projection_term_order AFTER INSERT OR UPDATE OR DELETE ON catalog.term_orders
 FOR EACH ROW EXECUTE FUNCTION catalog.invalidate_directory();
CREATE OR REPLACE TRIGGER invalidate_projection_term_truncate AFTER TRUNCATE ON catalog.term_orders
 FOR EACH STATEMENT EXECUTE FUNCTION catalog.invalidate_directory();

-- Only the short head change fences the revision. The application role retains
-- SELECT-only access to the global revision and cannot change its value.
CREATE OR REPLACE FUNCTION catalog.check_projection_source_revision() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE expected bigint; current_revision bigint;
BEGIN
 SELECT source_revision INTO expected FROM catalog.projection_generations
 WHERE version=NEW.version AND school_id=NEW.school_id AND build_step IS NOT NULL;
 IF FOUND THEN
  SELECT revision INTO current_revision FROM catalog.directory_revision WHERE singleton FOR SHARE NOWAIT;
  IF current_revision IS DISTINCT FROM expected THEN RAISE EXCEPTION 'projection_source_changed' USING ERRCODE='40001'; END IF;
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION catalog.check_projection_source_revision() FROM PUBLIC;
CREATE OR REPLACE TRIGGER check_projection_revision BEFORE INSERT OR UPDATE ON catalog.projection_heads
 FOR EACH ROW EXECUTE FUNCTION catalog.check_projection_source_revision();
