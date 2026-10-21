CREATE TABLE channel_invites (
    channel_id BIGINT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL CHECK (octet_length(token_hash) = 32),
    access TEXT NOT NULL CHECK (access IN ('guest', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
