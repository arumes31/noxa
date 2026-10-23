-- Bounded retention queries must not repeatedly sort the full history.
CREATE INDEX IF NOT EXISTS idx_audit_log_retention ON audit_log (created_at, id);
CREATE INDEX IF NOT EXISTS idx_offline_messages_retention
    ON offline_messages (delivered_at, id) WHERE delivered_at IS NOT NULL;
