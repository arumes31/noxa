-- Validate clean installations offline so serving processes need no DDL.
-- The legacy migration/backfill tooling still preserves unconverted rows;
-- production readiness rejects unvalidated constraints until conversion.
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM public.chat_messages
  WHERE body <> '' OR (deleted_at IS NULL AND (body_enc = '' OR key_id <= 0))) THEN
  ALTER TABLE public.chat_messages VALIDATE CONSTRAINT chat_messages_no_plaintext;
  ALTER TABLE public.chat_messages VALIDATE CONSTRAINT chat_messages_sealed;
 END IF;
END $$;
