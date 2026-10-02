-- An identity explicitly enrolled by a role manager has no account login.
-- Its display label must not reserve another account's login name.
ALTER TABLE users ADD COLUMN IF NOT EXISTS identity_display_name TEXT;
