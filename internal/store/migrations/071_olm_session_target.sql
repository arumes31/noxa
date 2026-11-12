-- voicx:no-transaction
-- 071_olm_session_target.sql: admission matches a session from either side;
-- the primary key serves the source side, this index the target side.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_olm_sessions_target
    ON public.olm_sessions (server, target_device, target_epoch, source_device, source_epoch, session_id);
