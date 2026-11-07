-- Signed device revocations (38). When an account's user revokes one of its
-- devices, the revoking device signs a statement over the revoked device's
-- keys (netproto.DeviceRevocationTranscript). The server stores it so the
-- key directory can hand it to contacts and to the account's other devices,
-- which verify it against the account's device chain and keep refusing the
-- device even if a later directory lists it as active again. Revocations
-- without a signature (administrator, idle expiry, integrations, older
-- clients) stay in user_devices only.
CREATE TABLE IF NOT EXISTS device_revocations (
    device_id        TEXT        PRIMARY KEY REFERENCES user_devices(device_id) ON DELETE CASCADE,
    principal        TEXT        NOT NULL,
    signer_device_id TEXT        NOT NULL CHECK (char_length(signer_device_id) BETWEEN 1 AND 64),
    signed_at        BIGINT      NOT NULL CHECK (signed_at > 0),
    reason           TEXT        NOT NULL CHECK (reason IN ('user')),
    signature        BYTEA       NOT NULL CHECK (octet_length(signature) = 64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_revocations_principal
    ON device_revocations (principal, created_at DESC);
