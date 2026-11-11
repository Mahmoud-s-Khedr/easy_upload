CREATE TABLE IF NOT EXISTS uploads (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    declared_length BIGINT NOT NULL,
    committed_offset BIGINT NOT NULL DEFAULT 0,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    checksum_algorithm TEXT NOT NULL,
    expected_full_checksum TEXT NOT NULL DEFAULT '',
    hash_state BYTEA NOT NULL,
    storage_backend TEXT NOT NULL,
    storage_handle BYTEA NOT NULL,
    artifact_key TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NULL,
    version BIGINT NOT NULL DEFAULT 1,
    part_count INTEGER NOT NULL DEFAULT 0,
    finalization_attempts INTEGER NOT NULL DEFAULT 0,
    next_finalization_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS uploads_state_next_finalization_idx
    ON uploads (state, next_finalization_at);

CREATE INDEX IF NOT EXISTS uploads_state_expires_idx
    ON uploads (state, expires_at);

CREATE TABLE IF NOT EXISTS upload_parts (
    upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    part_number INTEGER NOT NULL,
    start_offset BIGINT NOT NULL,
    end_offset_exclusive BIGINT NOT NULL,
    size_bytes BIGINT NOT NULL,
    checksum_sha256 TEXT NOT NULL DEFAULT '',
    backend_etag TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (upload_id, part_number)
);

CREATE TABLE IF NOT EXISTS outbox_events (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    artifact_key TEXT NOT NULL DEFAULT '',
    backend TEXT NOT NULL,
    declared_length BIGINT NOT NULL,
    checksum_sha256 TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ NULL,
    payload_json JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS outbox_events_occurred_idx
    ON outbox_events (occurred_at);

