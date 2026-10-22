-- An identity public key resolves challenge logins for accounts bound through
-- a password login, so one key must never map to two accounts. Ambiguous
-- bindings are dropped (each owner re-binds on the next password login)
-- before uniqueness is enforced.
UPDATE users SET public_key = NULL
WHERE public_key IN (
    SELECT public_key FROM users
    WHERE public_key IS NOT NULL AND public_key <> ''
    GROUP BY public_key HAVING COUNT(*) > 1
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_public_key_unique
    ON users (public_key) WHERE public_key IS NOT NULL AND public_key <> '';
