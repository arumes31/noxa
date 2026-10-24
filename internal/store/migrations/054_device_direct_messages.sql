-- Direct messages to every device (dm_devices_v1). A spooled copy names the
-- device it was sealed for; empty keeps the legacy account-wide routing.
ALTER TABLE offline_messages ADD COLUMN IF NOT EXISTS from_device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE offline_messages ADD COLUMN IF NOT EXISTS to_device_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS offline_messages_pending_device
    ON offline_messages (to_user_id, to_device_id) WHERE delivered_at IS NULL;

-- History copies for a newly linked device: ciphertext chunks sealed by the
-- sending device to the new device's key. Deleted on acknowledgement or
-- after 24 hours.
CREATE TABLE IF NOT EXISTS device_history_chunks (
    device_id      TEXT        NOT NULL REFERENCES user_devices(device_id) ON DELETE CASCADE,
    seq            INTEGER     NOT NULL CHECK (seq >= 0),
    from_device_id TEXT        NOT NULL,
    total          INTEGER     NOT NULL CHECK (total > 0),
    data           BYTEA       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, seq)
);
CREATE INDEX IF NOT EXISTS device_history_chunks_age ON device_history_chunks (created_at);
