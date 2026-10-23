-- Linked devices per account (mobile web app). principal is the account's
-- unique ID: a registered user's, or a linked guest's key-derived ID, in
-- which case user_id stays NULL. Identity keys stay unique forever, so a
-- revoked device key can never be linked again.
CREATE TABLE IF NOT EXISTS user_devices (
    id                 BIGSERIAL PRIMARY KEY,
    device_id          TEXT        NOT NULL UNIQUE,
    principal          TEXT        NOT NULL,
    user_id            BIGINT      REFERENCES users(id) ON DELETE CASCADE,
    kind               TEXT        NOT NULL CHECK (kind IN ('desktop', 'web')),
    name               TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    ed25519_public     TEXT        NOT NULL UNIQUE,
    x25519_public      TEXT        NOT NULL DEFAULT '',
    first_device       BOOLEAN     NOT NULL DEFAULT false,
    approved_by        TEXT        NOT NULL DEFAULT '',
    approval_signature BYTEA,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expiry_warned_at   TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    revoked_reason     TEXT        NOT NULL DEFAULT ''
        CHECK (revoked_reason IN ('', 'user', 'admin_reset', 'idle', 'integration'))
);

CREATE INDEX IF NOT EXISTS user_devices_active_principal
    ON user_devices (principal) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS user_devices_active_idle
    ON user_devices (last_seen_at) WHERE revoked_at IS NULL AND NOT first_device;
CREATE UNIQUE INDEX IF NOT EXISTS user_devices_single_first
    ON user_devices (principal) WHERE first_device AND revoked_at IS NULL;
