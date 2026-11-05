-- voicx:no-transaction
-- 066_files_uploader_index.sql: per-uploader quota aggregates without a full
-- table scan.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_files_uploader
    ON public.files (uploader);
