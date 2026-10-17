-- Shared read receipts of private groups. seen_message_id only advances while
-- the member's client opts in; read_message_id stays the private unread cursor.
ALTER TABLE private_conversation_members ADD COLUMN seen_message_id BIGINT NOT NULL DEFAULT 0 CHECK(seen_message_id >= 0);
