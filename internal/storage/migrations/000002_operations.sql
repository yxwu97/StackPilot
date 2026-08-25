CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    type TEXT NOT NULL CHECK (type IN ('start', 'stop', 'restart', 'service-restart', 'port-plan', 'refresh', 'analyze')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled')),
    idempotency_subject TEXT NOT NULL,
    route_key TEXT NOT NULL,
    idempotency_key TEXT,
    idempotency_expires_at TEXT,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    cancellable INTEGER NOT NULL CHECK (cancellable IN (0, 1)),
    cancel_requested_at TEXT,
    error_code TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0)
) STRICT;

CREATE UNIQUE INDEX operations_active_workspace_idx
    ON operations(workspace_id)
    WHERE state IN ('queued', 'running', 'cancelling');

CREATE UNIQUE INDEX operations_idempotency_idx
    ON operations(idempotency_subject, route_key, workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX operations_workspace_created_idx ON operations(workspace_id, created_at DESC, id DESC);

CREATE TABLE operation_steps (
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    step_no INTEGER NOT NULL CHECK (step_no > 0),
    step_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'skipped', 'cancelled')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    started_at TEXT,
    finished_at TEXT,
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
    error_code TEXT,
    detail_ref TEXT,
    PRIMARY KEY (operation_id, step_no),
    UNIQUE (operation_id, step_key)
) STRICT, WITHOUT ROWID;
