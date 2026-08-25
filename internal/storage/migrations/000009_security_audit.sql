CREATE TABLE auth_token_rotation (
    id TEXT PRIMARY KEY CHECK (id = 'pending'),
    token_id TEXT NOT NULL,
    token_hash TEXT NOT NULL CHECK (length(token_hash) BETWEEN 32 AND 1024),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('local_token', 'browser_session', 'system')),
    action TEXT NOT NULL CHECK (length(action) BETWEEN 3 AND 128),
    target_type TEXT NOT NULL CHECK (length(target_type) BETWEEN 2 AND 64),
    target_id TEXT,
    result TEXT NOT NULL CHECK (result IN ('accepted', 'succeeded', 'failed', 'denied')),
    trace_id TEXT NOT NULL CHECK (length(trace_id) BETWEEN 3 AND 64),
    operation_id TEXT,
    client_type TEXT NOT NULL CHECK (client_type IN ('cli', 'web', 'internal')),
    error_code TEXT,
    occurred_at TEXT NOT NULL
) STRICT;

CREATE INDEX audit_events_occurred_at_idx ON audit_events(occurred_at, id);
CREATE INDEX audit_events_action_idx ON audit_events(action, id);
CREATE INDEX audit_events_operation_id_idx ON audit_events(operation_id, id) WHERE operation_id IS NOT NULL;
