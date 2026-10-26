-- Extend the directory schema additively; only the final epoch-bound wire is accepted.
ALTER TABLE olm_directory_state ADD COLUMN account_epoch BIGINT NOT NULL DEFAULT 1 CHECK(account_epoch BETWEEN 1 AND 9007199254740991);
ALTER TABLE olm_directory_keys DROP CONSTRAINT olm_directory_keys_device_id_generation_fkey;
ALTER TABLE olm_directory_keys DROP CONSTRAINT olm_directory_keys_pkey;
ALTER TABLE olm_directory_keys ADD COLUMN account_epoch BIGINT NOT NULL DEFAULT 1;
ALTER TABLE olm_directory_keys ADD COLUMN principal TEXT;
ALTER TABLE olm_directory_keys ADD COLUMN server TEXT;
ALTER TABLE olm_directory_keys ADD COLUMN bundle BYTEA;
ALTER TABLE olm_directory_keys ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'available' CHECK(lifecycle IN ('available','reserved','admitted','received','closed'));
ALTER TABLE olm_directory_keys ADD COLUMN claimed_epoch BIGINT;
UPDATE olm_directory_keys k SET principal=d.principal,server=p.server,bundle=p.bundle,lifecycle=CASE WHEN k.claimed_by IS NULL THEN 'available' ELSE 'reserved' END,claimed_epoch=CASE WHEN k.claimed_by IS NULL THEN NULL ELSE 1 END FROM user_devices d,olm_directory_publications p WHERE d.device_id=k.device_id AND p.device_id=k.device_id AND p.generation=k.generation;
ALTER TABLE olm_directory_keys ALTER COLUMN principal SET NOT NULL;
ALTER TABLE olm_directory_keys ALTER COLUMN server SET NOT NULL;
ALTER TABLE olm_directory_keys ALTER COLUMN bundle SET NOT NULL;
ALTER TABLE olm_directory_keys ADD PRIMARY KEY(device_id,account_epoch,key_id);
-- One final, unreleased Olm contract. Actual legacy message tables are untouched.
CREATE TABLE olm_rosters(principal TEXT PRIMARY KEY, version BIGINT NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991));
INSERT INTO olm_rosters(principal) SELECT DISTINCT principal FROM user_devices;
CREATE TABLE olm_account_history(
 device_id TEXT NOT NULL,account_epoch BIGINT NOT NULL,identity TEXT NOT NULL,signing TEXT NOT NULL,
 key_highwater BIGINT NOT NULL,PRIMARY KEY(device_id,account_epoch)
);
CREATE TABLE olm_resets(device_id TEXT NOT NULL,transition_id TEXT NOT NULL,record BYTEA NOT NULL,receipt BYTEA NOT NULL,PRIMARY KEY(device_id,transition_id),CHECK(octet_length(record)<=32768));
CREATE TABLE olm_attempts(
 server TEXT NOT NULL,sender TEXT NOT NULL,device_id TEXT NOT NULL,account_epoch BIGINT NOT NULL,attempt_id TEXT NOT NULL,
 logical_id TEXT NOT NULL,manifest BYTEA NOT NULL,digest TEXT NOT NULL,deadline BIGINT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('staging','accepted','rejected','aborted')),receipt BYTEA NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(server,device_id,account_epoch,attempt_id),CHECK(octet_length(manifest)<=131072)
);
CREATE INDEX olm_attempt_source ON olm_attempts(device_id,state);
CREATE TABLE olm_parts(server TEXT NOT NULL,device_id TEXT NOT NULL,account_epoch BIGINT NOT NULL,attempt_id TEXT NOT NULL,part_number INT NOT NULL CHECK(part_number BETWEEN 0 AND 7),data BYTEA NOT NULL CHECK(octet_length(data)<=524288),
 PRIMARY KEY(server,device_id,account_epoch,attempt_id,part_number),
 FOREIGN KEY(server,device_id,account_epoch,attempt_id) REFERENCES olm_attempts(server,device_id,account_epoch,attempt_id));
CREATE TABLE olm_messages(
 id TEXT PRIMARY KEY,server TEXT NOT NULL,sender TEXT NOT NULL,device_id TEXT NOT NULL,account_epoch BIGINT NOT NULL,
 logical_id TEXT NOT NULL,manifest BYTEA NOT NULL,
 UNIQUE(server,sender,logical_id)
);
CREATE TABLE olm_deliveries(
 id TEXT PRIMARY KEY,message_id TEXT NOT NULL REFERENCES olm_messages(id),principal TEXT NOT NULL,device_id TEXT NOT NULL,
 account_epoch BIGINT NOT NULL,copy BYTEA NOT NULL,digest TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','received')),
 UNIQUE(message_id,device_id),CHECK(octet_length(copy)<=32768)
);
CREATE INDEX olm_delivery_queue ON olm_deliveries(device_id,state,id);
CREATE TABLE olm_sessions(
 server TEXT NOT NULL,source_device TEXT NOT NULL,source_epoch BIGINT NOT NULL,target_device TEXT NOT NULL,target_epoch BIGINT NOT NULL,
 session_id TEXT NOT NULL,claim_id TEXT NOT NULL,initial_delivery TEXT NOT NULL,
 source_bundle BYTEA NOT NULL,target_bundle BYTEA NOT NULL,
 PRIMARY KEY(server,source_device,source_epoch,target_device,target_epoch,session_id)
);

ALTER TABLE olm_attempts ADD COLUMN receipt_ack BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE olm_attempt_fences(server TEXT NOT NULL,device_id TEXT NOT NULL,account_epoch BIGINT NOT NULL,retired_through BIGINT NOT NULL DEFAULT 0,PRIMARY KEY(server,device_id,account_epoch));
