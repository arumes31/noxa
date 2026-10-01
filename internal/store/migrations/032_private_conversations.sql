CREATE TABLE private_conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK(char_length(name) BETWEEN 1 AND 80),
    owner_uid TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1 CHECK(revision > 0),
    epoch BIGINT NOT NULL DEFAULT 1 CHECK(epoch > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE private_conversation_members (
    conversation_id UUID NOT NULL REFERENCES private_conversations(id) ON DELETE CASCADE,
    unique_id TEXT NOT NULL REFERENCES users(unique_id) ON DELETE CASCADE,
    pending BOOLEAN NOT NULL,
    joined_epoch BIGINT NOT NULL,
    read_message_id BIGINT NOT NULL DEFAULT 0 CHECK(read_message_id >= 0),
    PRIMARY KEY(conversation_id, unique_id),
    CHECK((pending AND joined_epoch=0) OR (NOT pending AND joined_epoch>0))
);
CREATE INDEX private_conversation_member_lookup ON private_conversation_members(unique_id, conversation_id);
CREATE TABLE private_conversation_messages (
    id BIGSERIAL PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES private_conversations(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL CHECK(epoch > 0),
    from_uid TEXT NOT NULL,
    client_reference TEXT NOT NULL CHECK(length(client_reference) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(conversation_id, from_uid, client_reference)
);
CREATE INDEX private_conversation_history ON private_conversation_messages(conversation_id, id DESC);
CREATE TABLE private_conversation_envelopes (
    message_id BIGINT NOT NULL REFERENCES private_conversation_messages(id) ON DELETE CASCADE,
    recipient_uid TEXT NOT NULL,
    ciphertext TEXT NOT NULL CHECK(length(ciphertext) BETWEEN 56 AND 24000),
    PRIMARY KEY(message_id, recipient_uid)
);
