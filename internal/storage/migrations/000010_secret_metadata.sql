CREATE TABLE secret_metadata (
    system_id TEXT NOT NULL CHECK (length(system_id) BETWEEN 1 AND 63),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 63),
    provider TEXT NOT NULL CHECK (provider = 'dpapi-file'),
    version INTEGER NOT NULL CHECK (version >= 1),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (system_id, name)
) STRICT, WITHOUT ROWID;

CREATE INDEX secret_metadata_updated_at_idx ON secret_metadata(updated_at, system_id, name);
