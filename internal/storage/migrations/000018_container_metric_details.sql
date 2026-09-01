CREATE TABLE runtime_container_metric_samples (
    metric_sample_id INTEGER NOT NULL REFERENCES runtime_metric_samples(id) ON DELETE CASCADE,
    container_id TEXT NOT NULL CHECK (length(container_id) BETWEEN 12 AND 64),
    compose_service TEXT NOT NULL CHECK (length(compose_service) BETWEEN 1 AND 128),
    cpu_percent REAL NOT NULL CHECK (cpu_percent BETWEEN 0 AND 100),
    memory_bytes INTEGER NOT NULL CHECK (memory_bytes >= 0),
    PRIMARY KEY (metric_sample_id, container_id)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER runtime_container_metric_samples_parent_check
BEFORE INSERT ON runtime_container_metric_samples
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1 FROM runtime_metric_samples
    WHERE id = NEW.metric_sample_id AND source = 'compose' AND status = 'available'
)
BEGIN
    SELECT RAISE(ABORT, 'container metric parent must be an available Compose sample');
END;

CREATE INDEX runtime_container_metric_samples_service_idx
    ON runtime_container_metric_samples(compose_service, metric_sample_id);
