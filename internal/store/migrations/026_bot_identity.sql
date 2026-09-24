-- Identity metadata only. Existing permission grants do not become identity
-- attributes during the permission reset; an operator explicitly marks bots.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_bot BOOLEAN NOT NULL DEFAULT FALSE;
