-- Preserve the device keys used to encrypt an offline DM. Empty values retain
-- the legacy lookup behavior for messages queued before key binding existed.
ALTER TABLE offline_messages
    ADD COLUMN sender_public_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN recipient_public_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN client_msg_id TEXT NOT NULL DEFAULT '';
