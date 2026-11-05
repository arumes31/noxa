-- voicx:no-transaction
-- 065_files_sha_index.sql: content-hash dedup and quota lookups (FindFileBySHA,
-- FileContentUsage) without scanning every file in a channel.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_files_channel_sha_uploaded
    ON public.files (channel_id, sha256, uploaded_at DESC);
