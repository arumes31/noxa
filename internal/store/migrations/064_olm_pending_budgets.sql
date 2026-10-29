-- Pending-ciphertext reclamation and per-account budgets (Task 4 review P2-1).
-- Copies record their sender account and whether they are a control, so the
-- per-sender and control-reserve budgets need no manifest scan.
ALTER TABLE olm_deliveries ADD COLUMN sender TEXT;
ALTER TABLE olm_deliveries ADD COLUMN control BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE olm_deliveries d SET sender=m.sender,
  control=(convert_from(m.manifest,'UTF8')::jsonb->>'purpose'='control')
  FROM olm_messages m WHERE m.id=d.message_id;
CREATE INDEX olm_delivery_pending_recipient ON olm_deliveries(principal) WHERE state='pending';
CREATE INDEX olm_delivery_pending_sender ON olm_deliveries(sender) WHERE state='pending';
-- When an account epoch was replaced (reset); its undrained copies are closed
-- after a grace period.
ALTER TABLE olm_account_history ADD COLUMN retired_at TIMESTAMPTZ NOT NULL DEFAULT now();
