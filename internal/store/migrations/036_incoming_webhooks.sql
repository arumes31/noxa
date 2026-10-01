CREATE TABLE incoming_webhooks (
 id BIGSERIAL PRIMARY KEY,
 channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 creator_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 token_hash BYTEA NOT NULL CHECK(octet_length(token_hash)=32),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 rate_reset TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 rate_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX incoming_webhooks_channel ON incoming_webhooks(channel_id);
