-- Per-phone push filters (muted channels and conversations, chat level, DND
-- and quiet hours) as the netproto.PushFilters JSON. Empty pushes everything.
ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS filters JSONB NOT NULL DEFAULT '{}'::jsonb;
