-- Integration scopes (807) narrow what an integration account may do through
-- gRPC and ServerQuery; they never grant anything its roles do not. NULL keeps
-- the legacy behaviour (every integration API its roles allow).
ALTER TABLE users ADD COLUMN IF NOT EXISTS integration_scopes TEXT[];
