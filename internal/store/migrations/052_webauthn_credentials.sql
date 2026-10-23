-- Passkeys registered by linked web devices. user_handle is a random
-- per-account handle (never the unique ID); credential holds the verified
-- go-webauthn credential record including its sign counter. Revoking the
-- registering device deletes its passkeys.
CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id            BIGSERIAL   PRIMARY KEY,
    credential_id BYTEA       NOT NULL UNIQUE,
    principal     TEXT        NOT NULL,
    user_handle   BYTEA       NOT NULL CHECK (octet_length(user_handle) BETWEEN 16 AND 64),
    device_id     TEXT        NOT NULL REFERENCES user_devices(device_id) ON DELETE CASCADE,
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    credential    JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS webauthn_credentials_principal ON webauthn_credentials (principal);
CREATE INDEX IF NOT EXISTS webauthn_credentials_user_handle ON webauthn_credentials (user_handle);
