-- Retain ACK metadata and all pending ciphertext; only delivered payloads age out.
ALTER TABLE olm_deliveries ADD COLUMN received_at TIMESTAMPTZ;
-- Existing received rows start their 30-day grace now; do not guess earlier ACK time.
UPDATE olm_deliveries SET received_at=now() WHERE state='received';
CREATE INDEX olm_received_retention ON olm_deliveries(received_at,id) WHERE state='received' AND octet_length(copy)>0;
