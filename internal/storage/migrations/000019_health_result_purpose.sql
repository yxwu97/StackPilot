ALTER TABLE health_results
ADD COLUMN purpose TEXT NOT NULL DEFAULT 'readiness' CHECK (purpose IN ('readiness', 'liveness'));

CREATE INDEX health_results_instance_purpose_time_idx
    ON health_results(service_instance_id, purpose, checked_at DESC, id DESC);

CREATE TABLE health_hourly_aggregates_v19 (
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('readiness', 'liveness')),
    kind TEXT NOT NULL CHECK (kind IN ('process', 'tcp', 'http', 'compose')),
    bucket_start TEXT NOT NULL,
    check_count INTEGER NOT NULL CHECK (check_count > 0),
    success_count INTEGER NOT NULL CHECK (success_count BETWEEN 0 AND check_count),
    duration_total_ms INTEGER NOT NULL CHECK (duration_total_ms >= 0),
    duration_max_ms INTEGER NOT NULL CHECK (duration_max_ms >= 0),
    PRIMARY KEY (service_instance_id, purpose, kind, bucket_start)
) STRICT;

INSERT INTO health_hourly_aggregates_v19
    (service_instance_id,purpose,kind,bucket_start,check_count,success_count,duration_total_ms,duration_max_ms)
SELECT service_instance_id,'readiness',kind,bucket_start,check_count,success_count,duration_total_ms,duration_max_ms
FROM health_hourly_aggregates;

DROP TABLE health_hourly_aggregates;
ALTER TABLE health_hourly_aggregates_v19 RENAME TO health_hourly_aggregates;
