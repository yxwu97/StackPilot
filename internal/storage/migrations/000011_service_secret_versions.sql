CREATE TABLE service_instance_secret_versions (
    service_instance_id TEXT NOT NULL REFERENCES service_instances(id) ON DELETE CASCADE,
    environment_name TEXT NOT NULL CHECK (length(environment_name) BETWEEN 1 AND 32767),
    system_id TEXT NOT NULL CHECK (length(system_id) BETWEEN 1 AND 63),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 63),
    provider TEXT NOT NULL CHECK (provider = 'dpapi-file'),
    version INTEGER NOT NULL CHECK (version >= 1),
    resolved_at TEXT NOT NULL,
    PRIMARY KEY (service_instance_id, environment_name)
) STRICT, WITHOUT ROWID;

CREATE INDEX service_instance_secret_versions_key_idx
    ON service_instance_secret_versions(system_id, name, version);
