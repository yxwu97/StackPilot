ALTER TABLE service_instances
ADD COLUMN graceful_timeout_ms INTEGER NOT NULL DEFAULT 15000
CHECK (graceful_timeout_ms BETWEEN 1 AND 600000);
