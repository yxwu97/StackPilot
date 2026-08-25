CREATE TABLE health_hourly_aggregates (
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('process', 'tcp', 'http', 'compose')),
    bucket_start TEXT NOT NULL,
    check_count INTEGER NOT NULL CHECK (check_count > 0),
    success_count INTEGER NOT NULL CHECK (success_count BETWEEN 0 AND check_count),
    duration_total_ms INTEGER NOT NULL CHECK (duration_total_ms >= 0),
    duration_max_ms INTEGER NOT NULL CHECK (duration_max_ms >= 0),
    PRIMARY KEY (service_instance_id, kind, bucket_start)
) STRICT;

CREATE TABLE incidents (
    id TEXT PRIMARY KEY CHECK (length(id) = 30 AND substr(id, 1, 4) = 'inc_'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    system_instance_id TEXT REFERENCES system_instances(id) ON DELETE CASCADE,
    service_instance_id TEXT REFERENCES service_instances(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (length(kind) BETWEEN 3 AND 64),
    severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    state TEXT NOT NULL CHECK (state IN ('open', 'resolved')),
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
    occurrence_count INTEGER NOT NULL DEFAULT 1 CHECK (occurrence_count > 0),
    trigger_event_id INTEGER REFERENCES events(id) ON DELETE SET NULL,
    trigger_health_result_id INTEGER REFERENCES health_results(id) ON DELETE SET NULL,
    context_json TEXT NOT NULL CHECK (json_valid(context_json) AND json_type(context_json) = 'object'),
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    resolved_at TEXT
) STRICT;

CREATE UNIQUE INDEX incidents_open_fingerprint_idx ON incidents(fingerprint) WHERE state = 'open';
CREATE INDEX incidents_workspace_time_idx ON incidents(workspace_id, last_seen_at DESC, id DESC);
CREATE INDEX incidents_service_time_idx ON incidents(service_instance_id, last_seen_at DESC) WHERE service_instance_id IS NOT NULL;

CREATE TABLE incident_analyses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    engine TEXT NOT NULL CHECK (length(engine) BETWEEN 1 AND 64),
    schema_version TEXT NOT NULL CHECK (length(schema_version) BETWEEN 1 AND 32),
    result_json TEXT NOT NULL CHECK (json_valid(result_json) AND json_type(result_json) = 'object'),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX incident_analyses_incident_time_idx ON incident_analyses(incident_id, created_at DESC, id DESC);

CREATE TABLE service_restart_attempts (
    service_instance_id TEXT PRIMARY KEY REFERENCES service_instances(id) ON DELETE CASCADE,
    attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
    sequence_started_at TEXT NOT NULL,
    last_attempt_at TEXT,
    ready_since TEXT
) STRICT;
