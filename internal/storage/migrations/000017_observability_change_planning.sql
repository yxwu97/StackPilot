-- stackpilot:foreign-keys-off
CREATE TABLE operations_v17 (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    type TEXT NOT NULL CHECK (type IN ('start', 'stop', 'restart', 'service-restart', 'port-plan', 'refresh', 'analyze', 'change-plan', 'verified-restart')),
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

INSERT INTO operations_v17 SELECT * FROM operations;
DROP TABLE operations;
ALTER TABLE operations_v17 RENAME TO operations;

CREATE UNIQUE INDEX operations_active_workspace_idx
    ON operations(workspace_id)
    WHERE state IN ('queued', 'running', 'cancelling');
CREATE UNIQUE INDEX operations_idempotency_idx
    ON operations(idempotency_subject, route_key, workspace_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX operations_workspace_created_idx ON operations(workspace_id, created_at DESC, id DESC);

CREATE TABLE system_revision_snapshots (
    id TEXT PRIMARY KEY CHECK (length(id) = 30 AND substr(id, 1, 4) = 'rev_'),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    system_instance_id TEXT REFERENCES system_instances(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('running', 'workspace')),
    schema_version TEXT NOT NULL CHECK (length(schema_version) BETWEEN 1 AND 32),
    digest TEXT NOT NULL UNIQUE CHECK (length(digest) = 64),
    snapshot_json TEXT NOT NULL CHECK (
        json_valid(snapshot_json) AND
        json_type(snapshot_json) = 'object' AND
        length(CAST(snapshot_json AS BLOB)) <= 4194304
    ),
    created_at TEXT NOT NULL CHECK (substr(created_at, -1) = 'Z'),
    CHECK ((kind = 'running' AND system_instance_id IS NOT NULL) OR
           (kind = 'workspace' AND system_instance_id IS NULL))
) STRICT;

CREATE INDEX system_revision_snapshots_workspace_created_idx
    ON system_revision_snapshots(workspace_id, created_at DESC, id DESC);
CREATE INDEX system_revision_snapshots_instance_idx
    ON system_revision_snapshots(system_instance_id, created_at DESC)
    WHERE system_instance_id IS NOT NULL;

CREATE TABLE change_plans (
    id TEXT PRIMARY KEY CHECK (length(id) = 31 AND substr(id, 1, 5) = 'plan_'),
    created_by_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    system_id TEXT NOT NULL REFERENCES systems(id) ON DELETE RESTRICT,
    from_snapshot_id TEXT NOT NULL REFERENCES system_revision_snapshots(id) ON DELETE RESTRICT,
    to_snapshot_id TEXT NOT NULL REFERENCES system_revision_snapshots(id) ON DELETE RESTRICT,
    rule_version TEXT NOT NULL CHECK (length(rule_version) BETWEEN 1 AND 32),
    state TEXT NOT NULL CHECK (state IN ('ready', 'blocked')),
    risk TEXT NOT NULL CHECK (risk IN ('info', 'low', 'medium', 'high', 'blocked')),
    item_count INTEGER NOT NULL CHECK (item_count >= 0 AND item_count <= 10000),
    blocked_count INTEGER NOT NULL CHECK (blocked_count BETWEEN 0 AND item_count),
    result_schema_version TEXT NOT NULL CHECK (length(result_schema_version) BETWEEN 1 AND 32),
    result_digest TEXT NOT NULL CHECK (length(result_digest) = 64),
    result_json TEXT NOT NULL CHECK (
        json_valid(result_json) AND
        json_type(result_json) = 'object' AND
        length(CAST(result_json AS BLOB)) <= 4194304
    ),
    created_at TEXT NOT NULL CHECK (substr(created_at, -1) = 'Z'),
    CHECK ((state = 'blocked' AND risk = 'blocked' AND blocked_count > 0) OR
           (state = 'ready' AND risk <> 'blocked' AND blocked_count = 0)),
    CHECK (from_snapshot_id <> to_snapshot_id),
    UNIQUE (workspace_id, from_snapshot_id, to_snapshot_id, rule_version, result_digest)
) STRICT;

CREATE INDEX change_plans_workspace_created_idx
    ON change_plans(workspace_id, created_at DESC, id DESC);

CREATE TABLE runtime_metric_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('process-job', 'compose')),
    status TEXT NOT NULL CHECK (status IN ('available', 'unavailable', 'unsupported')),
    observed_at TEXT NOT NULL CHECK (substr(observed_at, -1) = 'Z'),
    interval_ms INTEGER NOT NULL CHECK (interval_ms BETWEEN 10000 AND 300000),
    cpu_total_ms INTEGER CHECK (cpu_total_ms IS NULL OR cpu_total_ms >= 0),
    cpu_percent REAL CHECK (cpu_percent IS NULL OR cpu_percent BETWEEN 0 AND 100),
    memory_bytes INTEGER CHECK (memory_bytes IS NULL OR memory_bytes >= 0),
    process_count INTEGER CHECK (process_count IS NULL OR process_count >= 0),
    container_count INTEGER CHECK (container_count IS NULL OR container_count >= 0),
    reason_code TEXT CHECK (reason_code IS NULL OR length(reason_code) BETWEEN 3 AND 128),
    CHECK ((status = 'available' AND reason_code IS NULL AND
            (cpu_total_ms IS NOT NULL OR memory_bytes IS NOT NULL OR process_count IS NOT NULL OR container_count IS NOT NULL)) OR
           (status <> 'available' AND reason_code IS NOT NULL AND cpu_total_ms IS NULL AND cpu_percent IS NULL AND
            memory_bytes IS NULL AND process_count IS NULL AND container_count IS NULL)),
    CHECK ((source = 'process-job' AND container_count IS NULL) OR
           (source = 'compose' AND process_count IS NULL)),
    UNIQUE (service_instance_id, source, observed_at)
) STRICT;

CREATE INDEX runtime_metric_samples_instance_time_idx
    ON runtime_metric_samples(service_instance_id, observed_at DESC, id DESC);
CREATE INDEX runtime_metric_samples_time_idx ON runtime_metric_samples(observed_at, id);

CREATE TABLE runtime_metric_hourly_aggregates (
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('process-job', 'compose')),
    bucket_start TEXT NOT NULL CHECK (substr(bucket_start, -1) = 'Z'),
    sample_count INTEGER NOT NULL CHECK (sample_count > 0),
    available_count INTEGER NOT NULL CHECK (available_count BETWEEN 0 AND sample_count),
    cpu_sample_count INTEGER NOT NULL CHECK (cpu_sample_count BETWEEN 0 AND available_count),
    cpu_min_percent REAL CHECK (cpu_min_percent IS NULL OR cpu_min_percent BETWEEN 0 AND 100),
    cpu_max_percent REAL CHECK (cpu_max_percent IS NULL OR cpu_max_percent BETWEEN 0 AND 100),
    cpu_total_percent REAL CHECK (cpu_total_percent IS NULL OR cpu_total_percent >= 0),
    memory_sample_count INTEGER NOT NULL CHECK (memory_sample_count BETWEEN 0 AND available_count),
    memory_min_bytes INTEGER CHECK (memory_min_bytes IS NULL OR memory_min_bytes >= 0),
    memory_max_bytes INTEGER CHECK (memory_max_bytes IS NULL OR memory_max_bytes >= 0),
    memory_total_bytes INTEGER CHECK (memory_total_bytes IS NULL OR memory_total_bytes >= 0),
    PRIMARY KEY (service_instance_id, source, bucket_start),
    CHECK ((cpu_sample_count = 0 AND cpu_min_percent IS NULL AND cpu_max_percent IS NULL AND cpu_total_percent IS NULL) OR
           (cpu_sample_count > 0 AND cpu_min_percent IS NOT NULL AND cpu_max_percent IS NOT NULL AND
            cpu_total_percent IS NOT NULL AND cpu_min_percent <= cpu_max_percent)),
    CHECK ((memory_sample_count = 0 AND memory_min_bytes IS NULL AND memory_max_bytes IS NULL AND memory_total_bytes IS NULL) OR
           (memory_sample_count > 0 AND memory_min_bytes IS NOT NULL AND memory_max_bytes IS NOT NULL AND
            memory_total_bytes IS NOT NULL AND memory_min_bytes <= memory_max_bytes))
) STRICT, WITHOUT ROWID;

CREATE INDEX runtime_metric_hourly_time_idx
    ON runtime_metric_hourly_aggregates(bucket_start, service_instance_id, source);
