CREATE TABLE IF NOT EXISTS catalog.teacher_portrait_exclusions (
 school_id text NOT NULL REFERENCES catalog.objects(id),
 digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
 site_ids text[] NOT NULL CHECK (cardinality(site_ids) BETWEEN 1 AND 100),
 PRIMARY KEY(school_id,digest)
);
REVOKE ALL ON catalog.teacher_portrait_exclusions FROM PUBLIC;

-- Old importers also pass this point before uploading any source image.
CREATE OR REPLACE FUNCTION catalog.check_teacher_site_portrait() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS (SELECT FROM catalog.teacher_portrait_exclusions WHERE school_id=NEW.school_id AND digest=NEW.photo_id) THEN
  RAISE EXCEPTION 'excluded site portrait' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END
$$;
CREATE OR REPLACE TRIGGER teacher_site_portrait_policy BEFORE INSERT OR UPDATE OF school_id,photo_id
 ON catalog.teacher_homepage_sites FOR EACH ROW WHEN (NEW.photo_id <> '')
 EXECUTE FUNCTION catalog.check_teacher_site_portrait();
