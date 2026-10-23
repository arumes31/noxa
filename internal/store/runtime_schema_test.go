package store

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestRuntimeSchemaReadOnlyAndPrivilegeBoundary(t *testing.T) {
	s := testScratchStore(t)
	ctx := context.Background()
	if err := s.EnsureFreshInstall(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckRuntimePrivileges(ctx); err == nil {
		t.Fatal("superuser accepted for runtime")
	}
	var database string
	if err := s.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("noxa_runtime_test_%d", time.Now().UnixNano())
	quoted := pq.QuoteIdentifier(role)
	password := "test-only-runtime-password"
	if _, err := s.db.ExecContext(ctx, `CREATE ROLE `+quoted+` LOGIN PASSWORD '`+password+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.ExecContext(context.Background(), `DROP OWNED BY `+quoted)
		_, _ = s.db.ExecContext(context.Background(), `DROP ROLE `+quoted)
	})
	grants := `REVOKE ALL ON DATABASE ` + pq.QuoteIdentifier(database) + ` FROM PUBLIC;
 GRANT CONNECT ON DATABASE ` + pq.QuoteIdentifier(database) + ` TO ` + quoted + `;
 REVOKE CREATE ON SCHEMA public FROM PUBLIC;
 GRANT USAGE ON SCHEMA public TO ` + quoted + `;
 GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ` + quoted + `;
 GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + quoted + `;
 REVOKE ALL ON public.schema_migrations,public.noxa_install_generation FROM ` + quoted + `;
 GRANT SELECT ON public.schema_migrations,public.noxa_install_generation TO ` + quoted
	if _, err := s.db.ExecContext(ctx, grants); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(testDBURL())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + database
	u.User = url.UserPassword(role, password)
	runtime, err := New(u.String(), testLogger(), 1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	if err := runtime.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckRuntimePrivileges(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT current_user`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	for _, object := range []string{"DATABASE " + pq.QuoteIdentifier(database), "SCHEMA public"} {
		if _, err := s.db.ExecContext(ctx, `ALTER `+object+` OWNER TO `+quoted+`; REVOKE ALL ON `+object+` FROM `+quoted); err != nil {
			t.Fatal(err)
		}
		if err := runtime.CheckRuntimePrivileges(ctx); err == nil {
			t.Fatalf("owner with revoked ACL accepted: %s", object)
		}
		if _, err := s.db.ExecContext(ctx, `ALTER `+object+` OWNER TO `+pq.QuoteIdentifier(owner)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, grants); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.ValidateChatNoPlaintext(ctx); err != nil {
		t.Fatal("runtime tried DDL", err)
	}
	for _, q := range []string{`CREATE TABLE public.runtime_ddl(x int)`, `CREATE TEMP TABLE runtime_ddl(x int)`, `DELETE FROM schema_migrations`, `ALTER TABLE users ADD COLUMN injected int`} {
		if _, err := runtime.db.ExecContext(ctx, q); err == nil {
			t.Fatalf("runtime privilege escaped: %s", q)
		}
	}
	if _, err := runtime.db.ExecContext(ctx, `INSERT INTO audit_log(actor_unique_id,action,target,detail) VALUES('test','test','','')`); err != nil {
		t.Fatal("runtime insert failed", err)
	}
	if _, err := runtime.db.ExecContext(ctx, `DELETE FROM audit_log WHERE action='test'`); err != nil {
		t.Fatal("retention delete failed", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE filename='050_validate_chat_constraints.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CheckSchema(ctx); err == nil {
		t.Fatal("missing migration accepted or silently applied")
	}
}
