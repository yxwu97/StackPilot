CREATE TABLE events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 3 AND 128),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    instance_id TEXT REFERENCES system_instances(id) ON DELETE CASCADE,
    service_instance_id TEXT REFERENCES service_instances(id) ON DELETE CASCADE,
    operation_id TEXT REFERENCES operations(id) ON DELETE CASCADE,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
    occurred_at TEXT NOT NULL
) STRICT;

CREATE INDEX events_workspace_id_idx ON events(workspace_id, id);
CREATE INDEX events_system_id_idx ON events(system_id, id);
CREATE INDEX events_instance_id_idx ON events(instance_id, id) WHERE instance_id IS NOT NULL;
CREATE INDEX events_operation_id_idx ON events(operation_id, id) WHERE operation_id IS NOT NULL;
CREATE INDEX events_occurred_at_idx ON events(occurred_at, id);
