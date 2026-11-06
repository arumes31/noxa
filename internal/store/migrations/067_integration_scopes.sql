-- Integration scopes (807) narrow what an integration account may do through
-- gRPC and ServerQuery; they never grant anything its roles do not. NULL keeps
-- the legacy behaviour (every integration API its roles allow).
ALTER TABLE users ADD COLUMN IF NOT EXISTS integration_scopes TEXT[];
-- Every ceiling change increments the revision. Integration principals carry
-- the revision they signed in under, so restoring an earlier ceiling cannot
-- make an older session valid again.
ALTER TABLE users ADD COLUMN IF NOT EXISTS integration_scope_revision BIGINT NOT NULL DEFAULT 0;
