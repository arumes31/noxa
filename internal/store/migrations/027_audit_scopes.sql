-- NULL marks legacy/unclassified records. Empty arrays explicitly mean server
-- scope. Free-form audit text cannot establish trusted authorization scope.
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS scope_channel_ids BIGINT[];
