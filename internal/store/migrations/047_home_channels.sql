-- The home anchor represents neutral control membership (channel zero).
ALTER TABLE channels ADD COLUMN IF NOT EXISTS system_kind TEXT NOT NULL DEFAULT ''
    CHECK (system_kind IN ('', 'home', 'public'));
CREATE UNIQUE INDEX IF NOT EXISTS channels_single_home ON channels(system_kind) WHERE system_kind='home';
CREATE OR REPLACE FUNCTION protect_home_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.system_kind='home' THEN
        IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Lobby cannot be deleted'; END IF;
        IF NEW.system_kind<>'home' OR NEW.parent_id IS NOT NULL OR NEW.name<>'Lobby'
            OR NEW.channel_type<>2 OR NEW.text_only OR COALESCE(NEW.password_hash,'')<>'' THEN
            RAISE EXCEPTION 'Lobby is a permanent home channel';
        END IF;
    END IF;
    RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
DROP TRIGGER IF EXISTS channels_protect_home ON channels;
CREATE TRIGGER channels_protect_home BEFORE UPDATE OR DELETE ON channels
    FOR EACH ROW EXECUTE FUNCTION protect_home_channel();
