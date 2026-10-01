-- Retire the numeric permission model after the roles-v1 cutover.
-- These tables and the admin flag are no longer read by the production server.
-- The operator-approved cutover does not preserve legacy grants or tokens.

DROP TABLE IF EXISTS channel_group_auto_rules;
DROP TABLE IF EXISTS channel_permissions;
DROP TABLE IF EXISTS channel_client_permissions;
DROP TABLE IF EXISTS client_permissions;
DROP TABLE IF EXISTS server_group_permissions;
DROP TABLE IF EXISTS channel_group_permissions;
DROP TABLE IF EXISTS server_group_members;
DROP TABLE IF EXISTS channel_group_members;
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS server_groups;
DROP TABLE IF EXISTS channel_groups;

ALTER TABLE users DROP COLUMN IF EXISTS is_admin;
ALTER TABLE channels DROP COLUMN IF EXISTS needed_join_power;
ALTER TABLE channels DROP COLUMN IF EXISTS inherit_permissions;
