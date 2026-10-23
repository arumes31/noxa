-- Scope zero is global chat, not a channel foreign key.
CREATE FUNCTION erase_deleted_channel_chat() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM chat_messages WHERE channel_id = OLD.id;
    DELETE FROM chat_scope_keys WHERE scope_id = OLD.id;
    DELETE FROM chat_scope_seq WHERE scope_id = OLD.id;
    RETURN OLD;
END $$;
CREATE TRIGGER channel_chat_erasure AFTER DELETE ON channels
FOR EACH ROW EXECUTE FUNCTION erase_deleted_channel_chat();

-- Lock the channel to close insert-versus-delete races for every writer.
CREATE FUNCTION require_live_chat_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE scope bigint;
BEGIN
    IF TG_TABLE_NAME = 'chat_messages' THEN scope := NEW.channel_id;
    ELSE scope := NEW.scope_id; END IF;
    IF scope <> 0 THEN
        PERFORM id FROM channels WHERE id = scope FOR KEY SHARE;
        IF NOT FOUND THEN RAISE EXCEPTION 'chat channel no longer exists' USING ERRCODE = '23503'; END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER live_message_channel BEFORE INSERT OR UPDATE OF channel_id ON chat_messages
FOR EACH ROW EXECUTE FUNCTION require_live_chat_scope();
CREATE TRIGGER live_key_channel BEFORE INSERT OR UPDATE OF scope_id ON chat_scope_keys
FOR EACH ROW EXECUTE FUNCTION require_live_chat_scope();
CREATE TRIGGER live_sequence_channel BEFORE INSERT OR UPDATE OF scope_id ON chat_scope_seq
FOR EACH ROW EXECUTE FUNCTION require_live_chat_scope();

-- Erase remnants of channels deleted before this invariant existed.
DELETE FROM chat_messages m WHERE m.channel_id <> 0 AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.id=m.channel_id);
DELETE FROM chat_scope_keys k WHERE k.scope_id <> 0 AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.id=k.scope_id);
DELETE FROM chat_scope_seq s WHERE s.scope_id <> 0 AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.id=s.scope_id);
