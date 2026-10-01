ALTER TABLE discussion_channels ADD COLUMN auto_archive_hours INTEGER NOT NULL DEFAULT 0 CHECK (auto_archive_hours IN (0,24,72,168));
ALTER TABLE discussion_threads ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE discussion_threads ADD COLUMN resolved BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE discussion_threads ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
UPDATE discussion_threads SET last_activity_at=updated_at;
CREATE INDEX discussion_threads_inactivity ON discussion_threads(channel_id,last_activity_at) WHERE NOT archived;
CREATE INDEX discussion_threads_pinned_page ON discussion_threads(channel_id,archived,pinned DESC,id DESC);
