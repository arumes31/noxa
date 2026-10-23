\set ON_ERROR_STOP on
-- Run offline as the database/migration owner after /out/migrate. Passwords
-- arrive via the operator process environment, never psql command arguments.
\getenv runtime_password NOXA_RUNTIME_PASSWORD
\getenv backup_password NOXA_BACKUP_PASSWORD
\if :{?runtime_password}
\else
  \echo 'NOXA_RUNTIME_PASSWORD is required'
  \quit 3
\endif
\if :{?backup_password}
\else
  \echo 'NOXA_BACKUP_PASSWORD is required'
  \quit 3
\endif
SELECT length(:'runtime_password') >= 24 AND length(:'backup_password') >= 24
 AND :'runtime_password' <> :'backup_password'
 AND current_user NOT IN ('noxa_runtime','noxa_backup') AS safe_input \gset
\if :safe_input
\else
  \echo 'Use the offline owner and distinct strong passwords of at least 24 characters'
  \quit 3
\endif
SELECT current_user AS owner_role \gset
SELECT format('CREATE ROLE %I', name) FROM (VALUES ('noxa_runtime'),('noxa_backup')) AS roles(name)
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=name) \gexec
SELECT format('ALTER ROLE noxa_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'runtime_password') \gexec
SELECT format('ALTER ROLE noxa_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'backup_password') \gexec
-- NOINHERIT alone still permits SET ROLE. Dedicated service accounts must
-- have no membership grants, including stale grants from an older setup.
SELECT format('REVOKE %I FROM %I', parent.rolname, child.rolname)
 FROM pg_auth_members m JOIN pg_roles parent ON parent.oid=m.roleid
 JOIN pg_roles child ON child.oid=m.member
 WHERE child.rolname IN ('noxa_runtime','noxa_backup') \gexec
REVOKE ALL ON DATABASE :"DBNAME" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"DBNAME" TO noxa_runtime, noxa_backup;
REVOKE CREATE ON SCHEMA public FROM PUBLIC, noxa_runtime, noxa_backup;
GRANT USAGE ON SCHEMA public TO noxa_runtime, noxa_backup;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM noxa_runtime, noxa_backup;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM noxa_runtime, noxa_backup;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO noxa_runtime;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO noxa_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO noxa_backup;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO noxa_backup;
REVOKE ALL ON public.schema_migrations, public.noxa_install_generation FROM noxa_runtime;
GRANT SELECT ON public.schema_migrations, public.noxa_install_generation TO noxa_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" IN SCHEMA public
 GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO noxa_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" IN SCHEMA public
 GRANT USAGE, SELECT ON SEQUENCES TO noxa_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" IN SCHEMA public
 GRANT SELECT ON TABLES TO noxa_backup;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner_role" IN SCHEMA public
 GRANT SELECT ON SEQUENCES TO noxa_backup;
ALTER ROLE noxa_runtime SET search_path = public, pg_temp;
ALTER ROLE noxa_backup SET search_path = public, pg_temp;
