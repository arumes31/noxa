-- A per-device copy spooled for the sender's own other devices keeps the
-- account it was addressed to, so those devices file it correctly.
ALTER TABLE offline_messages ADD COLUMN IF NOT EXISTS peer_unique_id TEXT NOT NULL DEFAULT '';
