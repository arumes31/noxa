-- 073_device_key_claim.sql: a device's own signature over its signing and
-- encryption key (netproto.DeviceKeyClaimTranscript with DeviceKeyClaimRef),
-- relayed to the account's other devices, which verify it before confirming
-- the device. Rows written before this migration have none until the device
-- signs in once with a client that publishes it.

ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS key_claim bytea CHECK (octet_length(key_claim) = 64);
