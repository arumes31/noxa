-- Additive staging schema. No legacy authority is translated and no role
-- configuration is created or activated by server startup/migration.
CREATE TABLE auth_roles (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    position INTEGER NOT NULL CHECK (position >= 0),
    color TEXT NOT NULL DEFAULT '',
    icon TEXT NOT NULL DEFAULT '',
    hoist BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (position) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE authorization_config (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    model TEXT NOT NULL DEFAULT 'roles-v1' CHECK (model = 'roles-v1'),
    revision BIGINT NOT NULL CHECK (revision > 0),
    active BOOLEAN NOT NULL DEFAULT FALSE,
    owner_id BIGINT NOT NULL REFERENCES users(id),
    everyone_role_id BIGINT NOT NULL REFERENCES auth_roles(id) DEFERRABLE INITIALLY DEFERRED,
    default_member_role_id BIGINT REFERENCES auth_roles(id) DEFERRABLE INITIALLY DEFERRED,
    CHECK (default_member_role_id IS NULL OR default_member_role_id <> everyone_role_id)
);

CREATE TABLE auth_role_grants (
    role_id BIGINT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    capability TEXT NOT NULL CHECK (capability <> ''),
    PRIMARY KEY (role_id, capability)
);

CREATE TABLE auth_member_roles (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id BIGINT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX auth_member_roles_role ON auth_member_roles(role_id);

CREATE TABLE auth_channel_access (
    channel_id BIGINT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    synced BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE auth_channel_role_overrides (
    channel_id BIGINT NOT NULL REFERENCES auth_channel_access(channel_id) ON DELETE CASCADE,
    role_id BIGINT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    capability TEXT NOT NULL CHECK (capability <> ''),
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    PRIMARY KEY (channel_id, role_id, capability)
);
CREATE INDEX auth_channel_role_overrides_role ON auth_channel_role_overrides(role_id);

CREATE TABLE auth_channel_member_overrides (
    channel_id BIGINT NOT NULL REFERENCES auth_channel_access(channel_id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    capability TEXT NOT NULL CHECK (capability <> ''),
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    PRIMARY KEY (channel_id, user_id, capability)
);
CREATE INDEX auth_channel_member_overrides_user ON auth_channel_member_overrides(user_id);
