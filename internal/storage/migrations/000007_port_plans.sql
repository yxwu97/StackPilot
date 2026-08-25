CREATE TABLE resolved_system_specs (
    digest TEXT PRIMARY KEY CHECK (length(digest) = 64),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    manifest_digest TEXT NOT NULL REFERENCES manifest_snapshots(digest) ON DELETE RESTRICT,
    spec_json TEXT NOT NULL CHECK (json_valid(spec_json) AND json_type(spec_json) = 'object'),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX resolved_system_specs_workspace_created_idx
    ON resolved_system_specs(workspace_id, created_at DESC, digest);

CREATE TABLE workspace_port_overrides (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    logical_name TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol = 'tcp'),
    host TEXT NOT NULL CHECK (host = '127.0.0.1'),
    port INTEGER NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, logical_name, protocol)
) STRICT, WITHOUT ROWID;

CREATE TABLE sticky_port_history (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    logical_name TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol = 'tcp'),
    host TEXT NOT NULL CHECK (host = '127.0.0.1'),
    port INTEGER NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    manifest_digest TEXT NOT NULL REFERENCES manifest_snapshots(digest) ON DELETE RESTRICT,
    succeeded_at TEXT NOT NULL,
    PRIMARY KEY (workspace_id, logical_name, protocol)
) STRICT, WITHOUT ROWID;

CREATE TABLE port_leases (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    instance_id TEXT REFERENCES system_instances(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    manifest_digest TEXT NOT NULL REFERENCES manifest_snapshots(digest) ON DELETE RESTRICT,
    logical_name TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol = 'tcp'),
    host TEXT NOT NULL CHECK (host = '127.0.0.1'),
    port INTEGER NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'bound', 'released', 'expired')),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (plan_id, logical_name)
) STRICT;

CREATE UNIQUE INDEX port_leases_active_endpoint_idx
    ON port_leases(protocol, host, port)
    WHERE state IN ('reserved', 'bound');

CREATE INDEX port_leases_workspace_state_idx
    ON port_leases(workspace_id, state, created_at DESC);

CREATE INDEX port_leases_instance_idx
    ON port_leases(instance_id, logical_name)
    WHERE instance_id IS NOT NULL;
