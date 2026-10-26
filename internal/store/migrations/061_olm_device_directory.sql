-- Immutable signed publications; watermarks survive payload expiry and prevent replay.
CREATE TABLE olm_directory_state (
 device_id TEXT PRIMARY KEY REFERENCES user_devices(device_id) ON DELETE CASCADE,
 identity TEXT NOT NULL,
 signing TEXT NOT NULL,
 generation BIGINT NOT NULL CHECK(generation > 0),
 key_highwater BIGINT NOT NULL,
 last_bundle BYTEA NOT NULL CHECK(octet_length(last_bundle)<=24576)
);
CREATE TABLE olm_directory_publications (
 device_id TEXT NOT NULL REFERENCES user_devices(device_id) ON DELETE CASCADE,
 generation BIGINT NOT NULL,
 server TEXT NOT NULL CHECK(server ~ '^tls-sha256:[0-9a-f]{64}$'),
 bundle BYTEA NOT NULL CHECK(octet_length(bundle)<=24576),
 expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(device_id,generation)
);
CREATE INDEX olm_directory_expiry ON olm_directory_publications(expires_at);
CREATE TABLE olm_directory_keys (
 device_id TEXT NOT NULL,
 generation BIGINT NOT NULL,
 key_id TEXT NOT NULL,
 claimed_by TEXT,
 claimed_device TEXT,
 claim_until BIGINT,
 request_id TEXT,
 claimed_at TIMESTAMPTZ,
 PRIMARY KEY(device_id,key_id),
 FOREIGN KEY(device_id,generation) REFERENCES olm_directory_publications(device_id,generation) ON DELETE CASCADE,
 UNIQUE(claimed_by,request_id)
);
CREATE INDEX olm_directory_claim_budget ON olm_directory_keys(claimed_by,claimed_at) WHERE claimed_by IS NOT NULL;
