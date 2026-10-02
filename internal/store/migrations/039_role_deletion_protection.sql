ALTER TABLE auth_roles ADD COLUMN deletion_protected BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE auth_roles SET deletion_protected = TRUE
WHERE id IN (SELECT role_id FROM auth_role_grants WHERE capability = 'administrator');
