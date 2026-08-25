ALTER TABLE service_instances
    ADD COLUMN process_mode TEXT NOT NULL DEFAULT 'daemon'
    CHECK (process_mode IN ('daemon', 'oneshot'));
