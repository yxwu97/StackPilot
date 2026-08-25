CREATE TABLE auth_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL CHECK (length(token_hash) BETWEEN 80 AND 512),
    created_at TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at TEXT
) STRICT;

CREATE UNIQUE INDEX auth_tokens_single_active_idx
    ON auth_tokens((1))
    WHERE revoked_at IS NULL;
