-- Two anonymous view totals cover the two Beijing dates intersecting a UTC day.
-- Keeping the overlap counts allows either calendar to be rebuilt after new writes.
CREATE TABLE IF NOT EXISTS catalog.activity_view_segments (
    kind text NOT NULL CHECK (kind IN ('course','teacher')),
    target_id text NOT NULL,
    utc_day date NOT NULL,
    early_views bigint NOT NULL CHECK (early_views >= 0),
    late_views bigint NOT NULL CHECK (late_views >= 0),
    PRIMARY KEY (kind, target_id, utc_day)
);
CREATE TABLE IF NOT EXISTS catalog.activity_calendar (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    beijing boolean NOT NULL DEFAULT false,
    seeded boolean NOT NULL DEFAULT false,
    switching boolean NOT NULL DEFAULT false,
    utc_view_floor date NOT NULL DEFAULT '0001-01-01',
    beijing_view_floor date NOT NULL DEFAULT '0001-01-01'
);
INSERT INTO catalog.activity_calendar(singleton) VALUES (true) ON CONFLICT DO NOTHING;
REVOKE ALL ON catalog.activity_view_segments, catalog.activity_calendar FROM PUBLIC;
-- Generic module-wide runtime grants still apply. With no policies, RLS limits
-- access to the migration owner and the owner's ledger trigger.
ALTER TABLE catalog.activity_view_segments ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog.activity_calendar ENABLE ROW LEVEL SECURITY;

-- Both executable versions write timestamped full snapshots in the view's
-- transaction. Only call this for actual changes in the canonical calendar.
CREATE OR REPLACE FUNCTION catalog.capture_view_segment() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE
    state catalog.activity_calendar%ROWTYPE;
    snapshot jsonb := NEW.data->'data';
    prior_views bigint;
    delta bigint;
    event_at timestamptz;
    event_utc_day date;
    expected_day date;
BEGIN
    SELECT * INTO STRICT state FROM catalog.activity_calendar WHERE singleton;
    IF state.switching THEN
        RETURN NULL;
    END IF;
    IF (NEW.data->>'type') <> (CASE WHEN state.beijing THEN 'discovery.activity.v2' ELSE 'discovery.activity.v1' END) THEN
        RAISE EXCEPTION 'catalog activity event calendar does not match storage';
    END IF;

    IF (snapshot->>'revision')::bigint = 1 THEN
        prior_views := 0;
    ELSE
        SELECT (h.data->'metrics'->>'views')::bigint INTO STRICT prior_views
        FROM catalog.activity_history h
        WHERE h.id = snapshot->>'id' AND h.revision = (snapshot->>'revision')::bigint - 1;
    END IF;
    delta := (snapshot->'metrics'->>'views')::bigint - prior_views;
    IF delta = 0 THEN
        RETURN NULL;
    END IF;

    -- Go emits nanoseconds while PostgreSQL rounds to microseconds. Truncate
    -- before parsing so the last nanosecond stays on its original date.
    event_at := regexp_replace(NEW.data->>'time', '([.][0-9]{6})[0-9]+', '\1')::timestamptz;
    event_utc_day := (event_at AT TIME ZONE 'UTC')::date;
    expected_day := CASE WHEN state.beijing THEN (event_at AT TIME ZONE 'Asia/Shanghai')::date ELSE event_utc_day END;
    IF delta < 0 OR expected_day IS DISTINCT FROM (snapshot->>'day')::date THEN
        RAISE EXCEPTION 'catalog view delta does not match its activity day';
    END IF;

    INSERT INTO catalog.activity_view_segments AS s(kind, target_id, utc_day, early_views, late_views)
    VALUES (snapshot->'target'->>'kind', snapshot->'target'->>'id', event_utc_day,
            CASE WHEN (event_at AT TIME ZONE 'Asia/Shanghai')::date = event_utc_day THEN delta ELSE 0 END,
            CASE WHEN (event_at AT TIME ZONE 'Asia/Shanghai')::date > event_utc_day THEN delta ELSE 0 END)
    ON CONFLICT (kind, target_id, utc_day) DO UPDATE
    SET early_views = s.early_views + EXCLUDED.early_views,
        late_views = s.late_views + EXCLUDED.late_views;
    RETURN NULL;
END;
$$;
REVOKE ALL ON FUNCTION catalog.capture_view_segment() FROM PUBLIC;
CREATE OR REPLACE TRIGGER capture_view_segment AFTER INSERT ON catalog.outbox FOR EACH ROW
WHEN ((NEW.data->>'type') IN ('discovery.activity.v1', 'discovery.activity.v2'))
EXECUTE FUNCTION catalog.capture_view_segment();

CREATE OR REPLACE FUNCTION catalog.seed_view_segments(cutover_at timestamptz) RETURNS void
LANGUAGE sql SET search_path = pg_catalog AS $$
    WITH events AS MATERIALIZED (
        SELECT data->>'subject' AS id, (data->'data'->>'revision')::bigint AS revision,
               (data->'data'->'metrics'->>'views')::bigint AS views,
               regexp_replace(data->>'time', '([.][0-9]{6})[0-9]+', '\1')::timestamptz AS event_at
        FROM catalog.outbox
        WHERE data->>'type' = 'discovery.activity.v1'
    ), proven AS (
        SELECT b.id, (e.event_at AT TIME ZONE 'Asia/Shanghai')::date > b.day AS late,
               e.views - CASE WHEN e.revision = 1 THEN 0 ELSE COALESCE(
                   (h.data->'metrics'->>'views')::bigint, prev.views,
                   -- Each legacy revision added at most one view.
                   CASE WHEN e.views = e.revision THEN e.views - 1 END) END AS delta
        FROM events e
        JOIN catalog.activity_buckets b ON b.id = e.id AND e.revision <= b.revision
        LEFT JOIN catalog.activity_history h ON h.id = e.id AND h.revision = e.revision - 1
        LEFT JOIN events prev ON prev.id = e.id AND prev.revision = e.revision - 1
        WHERE (e.event_at AT TIME ZONE 'UTC')::date = b.day
    ), known AS (
        SELECT id, COALESCE(sum(delta) FILTER (WHERE late), 0)::bigint AS late_views,
                   COALESCE(sum(delta) FILTER (WHERE NOT late), 0)::bigint AS early_views
        FROM proven WHERE delta > 0
        GROUP BY id
    ), unsplit AS (
        SELECT b.kind, b.target_id, b.day, b.first_count, COALESCE(k.late_views, 0) AS late_views,
               b.first_count - COALESCE(k.late_views, 0) - COALESCE(k.early_views, 0) AS unknown_views,
               GREATEST(0, LEAST(86400, EXTRACT(EPOCH FROM
                   (cutover_at AT TIME ZONE 'UTC') - b.day::timestamp))) AS elapsed_seconds
        FROM catalog.activity_buckets b LEFT JOIN known k USING (id)
        WHERE b.first_count > 0
    ), split AS (
        -- The ongoing UTC day can contribute only from the hours elapsed at cutover.
        SELECT *, late_views + round(unknown_views * GREATEST(elapsed_seconds - 57600, 0)
                                     / GREATEST(elapsed_seconds, 1))::bigint AS assigned_late
        FROM unsplit
    )
    INSERT INTO catalog.activity_view_segments(kind, target_id, utc_day, early_views, late_views)
    SELECT kind, target_id, day,
           CASE WHEN unknown_views >= 0 THEN first_count - assigned_late ELSE -1 END,
           assigned_late
    FROM split
$$;
REVOKE ALL ON FUNCTION catalog.seed_view_segments(timestamptz) FROM PUBLIC;

-- Publish complete snapshots with higher revisions, including zero buckets
-- whose views moved to another date. Older history remains available until its
-- normal retention sweep. The caller invalidates cursors across this cutover.
CREATE OR REPLACE FUNCTION catalog.project_view_segments(to_beijing boolean) RETURNS void
LANGUAGE sql SET search_path = pg_catalog AS $$
    WITH portions AS (
        SELECT kind, target_id, utc_day AS day, early_views AS views FROM catalog.activity_view_segments
        UNION ALL
        SELECT kind, target_id, utc_day + CASE WHEN to_beijing THEN 1 ELSE 0 END, late_views
        FROM catalog.activity_view_segments
    ), totals AS (
        SELECT kind, target_id, day, sum(views)::bigint AS views
        FROM portions WHERE views > 0 GROUP BY kind, target_id, day
    ), targets AS (
        SELECT kind, target_id, min(school_id) AS school_id, bool_or(visible) AS visible
        FROM catalog.activity_buckets GROUP BY kind, target_id
    ), planned AS (
        SELECT COALESCE(b.id,
                    t.kind || '/' || translate(encode(convert_to(t.target_id, 'UTF8'), 'base64'), E'+/=\n', '-_')
                        || '/' || to_char(t.day, 'YYYY-MM-DD')) AS id,
               COALESCE(b.kind, t.kind) AS kind, COALESCE(b.target_id, t.target_id) AS target_id,
               COALESCE(b.day, t.day) AS day, COALESCE(b.revision, 0) + 1 AS revision,
               COALESCE(t.views, 0) AS views, COALESCE(b.school_id, s.school_id) AS school_id,
               COALESCE(t.views > 0 AND s.visible, false) AS visible
        FROM catalog.activity_buckets b FULL JOIN totals t USING (kind, target_id, day)
        JOIN targets s ON s.kind = COALESCE(b.kind, t.kind) AND s.target_id = COALESCE(b.target_id, t.target_id)
    ), saved AS (
        INSERT INTO catalog.activity_buckets(id, kind, target_id, school_id, day, first_count, second_count, revision, visible, data)
        SELECT p.id, p.kind, p.target_id, p.school_id, p.day, p.views, 0, p.revision, p.visible,
               jsonb_build_object('source', 'catalog', 'id', p.id,
                   'target', jsonb_build_object('source', 'catalog', 'kind', p.kind, 'id', p.target_id),
                   'day', to_char(p.day, 'YYYY-MM-DD'), 'revision', p.revision, 'visible', p.visible,
                   'metrics', jsonb_build_object('views', p.views))
        FROM planned p
        ON CONFLICT (id) DO UPDATE
        SET first_count = EXCLUDED.first_count, revision = EXCLUDED.revision, visible = EXCLUDED.visible, data = EXCLUDED.data
        RETURNING id, revision, day, data
    ), history AS (
        INSERT INTO catalog.activity_history(id, revision, day, data)
        SELECT id, revision, day, data FROM saved RETURNING id, revision, data
    ), envelopes AS (
        SELECT 'activity:' || encode(sha256(convert_to(id, 'UTF8')), 'hex') || ':' || revision AS event_id,
               id, data FROM history
    )
    INSERT INTO catalog.outbox(id, data)
    SELECT event_id, jsonb_build_object('specversion', '1.0', 'id', event_id, 'source', 'catalog',
        'type', CASE WHEN to_beijing THEN 'discovery.activity.v2' ELSE 'discovery.activity.v1' END,
        'subject', id, 'time', statement_timestamp(), 'data', data)
    FROM envelopes
$$;
REVOKE ALL ON FUNCTION catalog.project_view_segments(boolean) FROM PUBLIC;

CREATE OR REPLACE FUNCTION catalog.use_beijing_activity_days(to_beijing boolean) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    state catalog.activity_calendar%ROWTYPE;
    view_floor date;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('catalog:view-retention', 0));
    SELECT * INTO STRICT state FROM catalog.activity_calendar WHERE singleton FOR UPDATE;
    IF state.beijing = to_beijing THEN
        RETURN;
    END IF;
    LOCK TABLE catalog.activity_buckets, catalog.activity_history, catalog.outbox IN SHARE ROW EXCLUSIVE MODE;
    IF NOT state.seeded THEN
        PERFORM catalog.seed_view_segments(clock_timestamp());
    END IF;
    IF EXISTS (
        SELECT FROM catalog.activity_view_segments
        WHERE early_views + late_views > 1000000000
           OR early_views > 1000000000 OR late_views > 1000000000
    ) OR EXISTS (SELECT FROM catalog.activity_buckets WHERE second_count <> 0) THEN
        RAISE EXCEPTION 'invalid catalog view count for activity calendar migration';
    END IF;

    UPDATE catalog.activity_calendar SET switching = true WHERE singleton;
    PERFORM catalog.project_view_segments(to_beijing);
    IF EXISTS (SELECT FROM catalog.activity_buckets WHERE first_count > 1000000000) THEN
        RAISE EXCEPTION 'projected catalog view count exceeds limit';
    END IF;
    -- New complete revisions supersede queued events from the previous
    -- calendar. In particular an old relay cannot read v2 after rollback.
    DELETE FROM catalog.outbox WHERE sent_at IS NULL
      AND data->>'type' = CASE WHEN to_beijing THEN 'discovery.activity.v1' ELSE 'discovery.activity.v2' END;
    UPDATE catalog.activity_retention SET revision = revision + 1 WHERE singleton;

    SELECT before_day INTO STRICT view_floor FROM catalog.view_retention WHERE singleton;
    UPDATE catalog.view_retention
    SET before_day = CASE WHEN to_beijing THEN GREATEST(state.beijing_view_floor, view_floor)
                          ELSE GREATEST(state.utc_view_floor, view_floor - 1) END
    WHERE singleton;
    UPDATE catalog.activity_calendar SET beijing = to_beijing, seeded = true, switching = false,
        utc_view_floor = CASE WHEN to_beijing THEN view_floor ELSE state.utc_view_floor END,
        beijing_view_floor = CASE WHEN to_beijing THEN state.beijing_view_floor ELSE view_floor END
    WHERE singleton;
    PERFORM pg_notify('catalog_outbox', '');
END;
$$;
REVOKE ALL ON FUNCTION catalog.use_beijing_activity_days(boolean) FROM PUBLIC;
SELECT catalog.use_beijing_activity_days(true);
