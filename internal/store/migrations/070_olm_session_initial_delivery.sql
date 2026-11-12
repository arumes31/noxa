-- voicx:no-transaction
-- 070_olm_session_initial_delivery.sql: every delivered DM copy's
-- acknowledgement and the pending-control expiry look olm_sessions up by
-- initial_delivery; without an index each one scanned the whole table, which
-- grows with every device pair.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_olm_sessions_initial_delivery
    ON public.olm_sessions (initial_delivery);
