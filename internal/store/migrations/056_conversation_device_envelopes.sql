-- Private groups reach every device: besides the account-wide envelope a
-- message carries one envelope per confirmed device other than the
-- account's first desktop. The sending device lets readers pick its key.
ALTER TABLE private_conversation_messages ADD COLUMN IF NOT EXISTS sender_device_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS private_conversation_device_envelopes (
    message_id    BIGINT NOT NULL REFERENCES private_conversation_messages(id) ON DELETE CASCADE,
    recipient_uid TEXT   NOT NULL,
    device_id     TEXT   NOT NULL,
    ciphertext    TEXT   NOT NULL CHECK (length(ciphertext) BETWEEN 56 AND 24000),
    PRIMARY KEY (message_id, recipient_uid, device_id)
);
