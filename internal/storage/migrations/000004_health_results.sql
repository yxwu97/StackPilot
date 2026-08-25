CREATE TABLE health_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('process', 'tcp', 'http')),
    success INTEGER NOT NULL CHECK (success IN (0, 1)),
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    error_code TEXT,
    summary TEXT NOT NULL CHECK (length(summary) <= 2048),
    checked_at TEXT NOT NULL,
    CHECK ((success = 1 AND error_code IS NULL) OR (success = 0 AND error_code IS NOT NULL))
) STRICT;

CREATE INDEX health_results_instance_time_idx
    ON health_results(service_instance_id, checked_at DESC, id DESC);
