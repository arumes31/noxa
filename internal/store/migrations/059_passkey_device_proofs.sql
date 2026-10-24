ALTER TABLE webauthn_credentials ADD COLUMN dm_certificate bytea CHECK (octet_length(dm_certificate) <= 32768);
ALTER TABLE user_devices ADD COLUMN passkey_proof bytea CHECK (octet_length(passkey_proof) <= 32768);
