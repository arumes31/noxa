package store

import (
	"context"
	"errors"
	"fmt"
)

// CheckSchema is read-only: a serving process never creates a migration ledger,
// changes checksums, or applies DDL. Offline migrate owns those operations.
func (s *Store) CheckSchema(ctx context.Context) error {
	if err := s.CheckFreshInstall(ctx); err != nil {
		return err
	}
	migrations, err := loadEmbeddedMigrations()
	if err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return fmt.Errorf("reading migration ledger; run noxa-migrate offline: %w", err)
	}
	if len(applied) != len(migrations) {
		return errors.New("database schema differs from this binary; run the matching noxa-migrate offline")
	}
	expected := make(map[string]string, len(migrations))
	for _, migration := range migrations {
		expected[migration.filename] = migration.checksum
	}
	for _, record := range applied {
		checksum, ok := expected[record.filename]
		if !ok || !record.checksum.Valid || checksum != record.checksum.String {
			return fmt.Errorf("migration %s is missing or has checksum drift; inspect with noxa-migrate offline", record.filename)
		}
	}
	var sealed bool
	if err := conn.QueryRowContext(ctx, `SELECT count(*) = 2 AND bool_and(convalidated)
        FROM pg_catalog.pg_constraint WHERE conrelid = 'public.chat_messages'::regclass
        AND conname IN ('chat_messages_no_plaintext', 'chat_messages_sealed')`).Scan(&sealed); err != nil {
		return err
	}
	if !sealed {
		return errors.New("chat plaintext constraints require offline backfill and validation")
	}
	return nil
}

// CheckRuntimePrivileges fails closed for owner/superuser connections, role
// escalation, schema creation and mutable migration ledgers. DML and sequence
// privileges remain sufficient for normal operation, including retention.
func (s *Store) CheckRuntimePrivileges(ctx context.Context) error {
	const query = `SELECT
 EXISTS (SELECT 1 FROM pg_catalog.pg_roles r WHERE
   pg_catalog.pg_has_role(current_user, r.oid, 'MEMBER') AND
   (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls
    OR r.rolname IN ('pg_read_server_files','pg_write_server_files','pg_execute_server_program')))
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_database d WHERE d.datname=current_database()
   AND pg_catalog.pg_has_role(current_user,d.datdba,'MEMBER'))
 OR pg_catalog.has_database_privilege(current_user, current_database(), 'CREATE')
 OR pg_catalog.has_database_privilege(current_user, current_database(), 'TEMP')
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
   AND (pg_catalog.has_schema_privilege(current_user, n.oid, 'CREATE')
     OR pg_catalog.pg_has_role(current_user,n.nspowner,'MEMBER')))
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND pg_catalog.pg_has_role(current_user,c.relowner,'MEMBER'))
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
   WHERE n.nspname='public' AND pg_catalog.pg_has_role(current_user,p.proowner,'MEMBER'))
 OR pg_catalog.has_table_privilege(current_user, 'public.schema_migrations', 'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
 OR pg_catalog.has_table_privilege(current_user, 'public.noxa_install_generation', 'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')`
	var unsafe bool
	if err := s.db.QueryRowContext(ctx, query).Scan(&unsafe); err != nil {
		return fmt.Errorf("checking runtime database privileges: %w", err)
	}
	if unsafe {
		return errors.New("runtime database role has administrative privileges; use the separate DML-only role and keep migration credentials offline")
	}
	return nil
}
