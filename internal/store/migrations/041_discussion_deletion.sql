-- Keep request IDs after moderation so delayed retries cannot recreate content.
ALTER TABLE discussion_threads ADD COLUMN deleted_at TIMESTAMPTZ;
ALTER TABLE discussion_messages ADD COLUMN deleted_at TIMESTAMPTZ;
