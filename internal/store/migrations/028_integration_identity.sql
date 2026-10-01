-- Integration eligibility grants no permissions. Existing accounts, including
-- legacy administrators and bots, require explicit operator opt-in.
ALTER TABLE users ADD COLUMN IF NOT EXISTS integration_enabled BOOLEAN NOT NULL DEFAULT FALSE;
