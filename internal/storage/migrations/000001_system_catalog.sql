CREATE TABLE systems (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    current_digest TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE manifest_snapshots (
    digest TEXT PRIMARY KEY,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    api_version TEXT NOT NULL,
    normalized_yaml TEXT NOT NULL,
    parsed_json TEXT NOT NULL CHECK (json_valid(parsed_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    root_path TEXT NOT NULL,
    canonical_path TEXT NOT NULL UNIQUE,
    manifest_status TEXT NOT NULL CHECK (manifest_status IN ('valid', 'invalid')),
    last_valid_digest TEXT REFERENCES manifest_snapshots(digest) ON DELETE RESTRICT,
    last_error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX workspaces_system_id_idx ON workspaces(system_id);

CREATE TABLE services (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL,
    driver TEXT NOT NULL CHECK (driver IN ('process', 'compose')),
    mode TEXT NOT NULL CHECK (mode IN ('daemon', 'oneshot')),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    definition_digest TEXT NOT NULL,
    PRIMARY KEY (workspace_id, service_id)
) STRICT, WITHOUT ROWID;
