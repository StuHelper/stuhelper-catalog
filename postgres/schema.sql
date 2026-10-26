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
CREATE TABLE IF NOT EXISTS catalog.audit(id text PRIMARY KEY, data jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS catalog.outbox(id text PRIMARY KEY, data jsonb NOT NULL, delivered boolean NOT NULL DEFAULT false);
REVOKE ALL ON SCHEMA catalog FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA catalog FROM PUBLIC;
