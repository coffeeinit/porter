-- Porter PostgreSQL foundation, version 2. Durable resource events.
-- Applied explicitly by the migration runner; no startup migration is performed.

CREATE TABLE porter_control_plane.resource_events (
    id TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    event_type TEXT NOT NULL CHECK (length(btrim(event_type)) BETWEEN 1 AND 128),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    project_id TEXT,
    service_id TEXT,
    release_id TEXT,
    instance_id TEXT,
    fleet_node_id TEXT,
    data JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object')
);

-- Event history intentionally has no resource foreign keys: it must survive
-- later resource retention or cleanup without cascading away audit history.
CREATE INDEX resource_events_page_idx
    ON porter_control_plane.resource_events (occurred_at DESC, id DESC);

CREATE INDEX resource_events_fleet_node_page_idx
    ON porter_control_plane.resource_events (fleet_node_id, occurred_at DESC, id DESC)
    WHERE fleet_node_id IS NOT NULL;

CREATE FUNCTION porter_control_plane.reject_resource_event_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'resource events are append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER resource_events_append_only
BEFORE UPDATE OR DELETE ON porter_control_plane.resource_events
FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_resource_event_mutation();
