CREATE TABLE workspace_drafts (
    id TEXT PRIMARY KEY CHECK (length(id) = 38 AND substr(id, 1, 6) = 'draft_'),
    kind TEXT NOT NULL CHECK (kind IN ('import', 'edit', 'relink')),
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    canonical_path TEXT NOT NULL,
    target_key TEXT NOT NULL CHECK (length(target_key) = 64),
    entry_script TEXT,
    source_digest TEXT CHECK (source_digest IS NULL OR length(source_digest) = 64),
    base_manifest_digest TEXT CHECK (base_manifest_digest IS NULL OR length(base_manifest_digest) = 64),
    state TEXT NOT NULL CHECK (state IN ('active', 'applied', 'expired')),
    draft_json TEXT NOT NULL CHECK (json_valid(draft_json) AND json_type(draft_json) = 'object'),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    applied_at TEXT,
    CHECK ((kind = 'import' AND workspace_id IS NULL AND base_manifest_digest IS NULL) OR
           (kind IN ('edit', 'relink') AND workspace_id IS NOT NULL AND base_manifest_digest IS NOT NULL))
) STRICT;

CREATE INDEX workspace_drafts_target_created_idx
    ON workspace_drafts(target_key, created_at DESC);
CREATE INDEX workspace_drafts_expiry_idx
    ON workspace_drafts(state, expires_at);

CREATE TABLE workspace_import_operations (
    id TEXT PRIMARY KEY CHECK (length(id) = 29 AND substr(id, 1, 3) = 'op_'),
    draft_id TEXT NOT NULL REFERENCES workspace_drafts(id) ON DELETE RESTRICT,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    target_key TEXT NOT NULL CHECK (length(target_key) = 64),
    candidate_id TEXT NOT NULL CHECK (length(candidate_id) BETWEEN 1 AND 64),
    type TEXT NOT NULL CHECK (type IN ('workspace-import-apply', 'workspace-edit-apply', 'workspace-relink-apply')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    idempotency_subject TEXT NOT NULL,
    route_key TEXT NOT NULL,
    idempotency_key TEXT,
    idempotency_expires_at TEXT,
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    error_code TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0)
) STRICT;

CREATE UNIQUE INDEX workspace_import_operations_active_target_idx
    ON workspace_import_operations(target_key)
    WHERE state IN ('queued', 'running');
CREATE UNIQUE INDEX workspace_import_operations_idempotency_idx
    ON workspace_import_operations(idempotency_subject, route_key, target_key, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX workspace_import_operations_created_idx
    ON workspace_import_operations(created_at DESC, id DESC);

CREATE TABLE workspace_import_operation_steps (
    operation_id TEXT NOT NULL REFERENCES workspace_import_operations(id) ON DELETE CASCADE,
    step_no INTEGER NOT NULL CHECK (step_no > 0),
    step_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'skipped', 'cancelled')),
    started_at TEXT,
    finished_at TEXT,
    error_code TEXT,
    PRIMARY KEY (operation_id, step_no),
    UNIQUE (operation_id, step_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE workspace_sources (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('existing-manifest', 'bat-import', 'structured-edit', 'relinked-manifest')),
    entry_script TEXT,
    source_digest TEXT CHECK (source_digest IS NULL OR length(source_digest) = 64),
    analyzed_at TEXT,
    updated_at TEXT NOT NULL
) STRICT;
