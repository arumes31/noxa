-- Text channels carry chat (read and written through channel subscriptions)
-- but never voice membership. Existing channels stay voice channels.
ALTER TABLE channels ADD COLUMN text_only BOOLEAN NOT NULL DEFAULT FALSE;
