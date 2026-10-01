//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"go.uber.org/zap"

	"noxa/internal/store"
)

func setupInspectionDatabase(t *testing.T) (*store.Store, string) {
	t.Helper()
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("test database URL is invalid")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("noxa_setup_inspect_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+pq.QuoteIdentifier(name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE `+pq.QuoteIdentifier(name)); err != nil {
			t.Errorf("dropping test database: %v", err)
		}
	})
	address.Path = "/" + name
	db, err := store.New(address.String(), zap.NewNop(), 3, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, address.String()
}

func databaseDigests(t *testing.T, db *sql.DB) map[string][32]byte {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	digests := make(map[string][32]byte, len(tables))
	for _, table := range tables {
		var contents string
		q := `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + pq.QuoteIdentifier(table) + ` t`
		if err := db.QueryRowContext(t.Context(), q).Scan(&contents); err != nil {
			t.Fatal(err)
		}
		digests[table] = sha256.Sum256([]byte(contents))
	}
	return digests
}

func TestInspectionCommandAgainstReadOnlyPostgres(t *testing.T) {
	db, dsn := setupInspectionDatabase(t)
	if err := db.EnsureFreshInstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil { // Fixture setup only; the command must never migrate.
		t.Fatal(err)
	}
	const fixture = `
	INSERT INTO users(unique_id,nickname,password_hash,public_key) VALUES('owner-uid','Owner','secret-password','secret-key');
	INSERT INTO channels(name,channel_type) VALUES('Private',2);
	INSERT INTO chat_messages(scope,from_unique_id,body_enc,key_id) VALUES(0,'owner-uid','secret-ciphertext',1);
	INSERT INTO chat_scope_keys(scope_id,key_id,wrapped_key) VALUES(0,1,convert_to('secret-wrapped-key','UTF8'));
	INSERT INTO audit_log(action,detail) VALUES('fixture','secret-audit-detail');`
	if _, err := db.DB().ExecContext(t.Context(), fixture); err != nil {
		t.Fatal(err)
	}
	before := databaseDigests(t, db.DB())
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := address.Query()
	query.Set("options", "-c default_transaction_read_only=on")
	address.RawQuery = query.Encode()
	var output, diagnostics bytes.Buffer
	code := run(t.Context(), []string{"-owner-uid", "owner-uid"}, func(string) string { return address.String() }, &output, &diagnostics, inspectDatabase, activateDatabase)
	if code != 0 {
		t.Fatalf("inspection failed: %s", &diagnostics)
	}
	if strings.Contains(output.String()+diagnostics.String(), "secret-") {
		t.Fatal("inspection exposed fixture secrets")
	}
	var report store.RoleSetupReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "unprepared" || !report.Owner.PasswordConfigured || !report.Owner.IdentityKeyConfigured || report.Counts["users"] != 1 {
		t.Fatalf("incorrect report: %+v", report)
	}
	if !reflect.DeepEqual(before, databaseDigests(t, db.DB())) {
		t.Fatal("inspection changed database contents")
	}
}

func TestInspectionCommandDoesNotInitializeEmptyDatabase(t *testing.T) {
	db, dsn := setupInspectionDatabase(t)
	var output, diagnostics bytes.Buffer
	code := run(t.Context(), []string{"-owner-uid", "owner-uid"}, func(string) string { return dsn }, &output, &diagnostics, inspectDatabase, activateDatabase)
	if code != 1 || output.Len() != 0 || !strings.Contains(diagnostics.String(), "fresh PostgreSQL database") {
		t.Fatalf("code=%d stderr=%s", code, &diagnostics)
	}
	if len(databaseDigests(t, db.DB())) != 0 {
		t.Fatal("command initialized the empty database")
	}
}
