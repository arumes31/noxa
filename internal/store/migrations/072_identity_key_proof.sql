-- 072_identity_key_proof.sql: an identity key a password login presented
-- without proving it (older desktop clients) still serves that session, but
-- never routes a later key login. Rows written before this migration were
-- bound under the old rules and stay proven (existing users keep working);
-- the server writes the flag explicitly on every new binding or device.

ALTER TABLE users ADD COLUMN IF NOT EXISTS public_key_proven BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS key_proven BOOLEAN NOT NULL DEFAULT TRUE;
