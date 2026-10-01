-- Scope zero stores global and encrypted private-conversation attachments.
-- Keep its integer scope for lookup, uniqueness, and quota accounting while
-- retaining referential integrity and cascade deletion for real channels.
ALTER TABLE files
    ADD COLUMN channel_ref BIGINT GENERATED ALWAYS AS (NULLIF(channel_id, 0)) STORED;

ALTER TABLE files
    ADD CONSTRAINT files_channel_ref_fkey
    FOREIGN KEY (channel_ref) REFERENCES channels(id) ON DELETE CASCADE;

CREATE INDEX idx_files_channel_ref ON files (channel_ref)
    WHERE channel_ref IS NOT NULL;

ALTER TABLE files DROP CONSTRAINT files_channel_id_fkey;
