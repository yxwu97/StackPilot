CREATE TABLE system_instances (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    manifest_digest TEXT NOT NULL REFERENCES manifest_snapshots(digest) ON DELETE RESTRICT,
    resolved_spec_digest TEXT NOT NULL CHECK (length(resolved_spec_digest) = 64),
    state TEXT NOT NULL CHECK (state IN ('stopping', 'failed', 'starting', 'degraded', 'running', 'stopped')),
    started_at TEXT NOT NULL,
    stopped_at TEXT,
    last_reconciled_at TEXT
) STRICT;

CREATE UNIQUE INDEX system_instances_active_workspace_idx
    ON system_instances(workspace_id)
    WHERE state <> 'stopped';

CREATE INDEX system_instances_workspace_started_idx
    ON system_instances(workspace_id, started_at DESC, id DESC);

CREATE TABLE service_instances (
    id TEXT PRIMARY KEY,
    system_instance_id TEXT NOT NULL REFERENCES system_instances(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('stopped', 'waiting_dependency', 'starting', 'waiting_ready', 'ready', 'degraded', 'completed', 'stopping', 'failed', 'unknown')),
    pid INTEGER CHECK (pid IS NULL OR pid > 0),
    process_started_at TEXT,
    executable_path TEXT,
    command_digest TEXT CHECK (command_digest IS NULL OR length(command_digest) = 64),
    platform_token TEXT,
    exit_code INTEGER,
    state_version INTEGER NOT NULL DEFAULT 1 CHECK (state_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (system_instance_id, service_id)
) STRICT;

CREATE INDEX service_instances_system_state_idx
    ON service_instances(system_instance_id, state, service_id);

CREATE TABLE log_segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    stream TEXT NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    path TEXT NOT NULL UNIQUE,
    first_sequence INTEGER NOT NULL CHECK (first_sequence > 0),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= first_sequence),
    first_timestamp TEXT NOT NULL,
    last_timestamp TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes > 0),
    closed_at TEXT NOT NULL
) STRICT;

CREATE INDEX log_segments_instance_sequence_idx
    ON log_segments(service_instance_id, first_sequence, last_sequence);
