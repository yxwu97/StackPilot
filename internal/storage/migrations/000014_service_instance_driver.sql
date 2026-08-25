ALTER TABLE service_instances
    ADD COLUMN driver TEXT NOT NULL DEFAULT 'process'
    CHECK (driver IN ('process', 'compose'));

ALTER TABLE service_instances
    ADD COLUMN compose_project_token TEXT
    CHECK (compose_project_token IS NULL OR (
        driver = 'compose' AND
        length(compose_project_token) BETWEEN 1 AND 65536
    ));
