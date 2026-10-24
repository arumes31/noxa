-- Web Push subscriptions, one per linked web device. Payloads never carry
-- message bodies; hide_details also drops sender and channel names.
CREATE TABLE IF NOT EXISTS push_subscriptions (
    device_id     TEXT        PRIMARY KEY REFERENCES user_devices(device_id) ON DELETE CASCADE,
    principal     TEXT        NOT NULL,
    endpoint      TEXT        NOT NULL UNIQUE CHECK (char_length(endpoint) BETWEEN 9 AND 1024),
    p256dh        BYTEA       NOT NULL,
    auth_secret   BYTEA       NOT NULL,
    hide_details  BOOLEAN     NOT NULL DEFAULT false,
    always_notify BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    failures      INTEGER     NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS push_subscriptions_principal ON push_subscriptions (principal);
