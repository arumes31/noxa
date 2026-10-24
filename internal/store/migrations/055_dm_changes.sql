-- Direct-message edits and deletions travel as per-device copies; spooled
-- rows keep which change they carry and which message they change.
ALTER TABLE offline_messages ADD COLUMN IF NOT EXISTS dm_action TEXT NOT NULL DEFAULT ''
    CHECK (dm_action IN ('', 'edit', 'delete'));
ALTER TABLE offline_messages ADD COLUMN IF NOT EXISTS ref_client_msg_id TEXT NOT NULL DEFAULT '';
