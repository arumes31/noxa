//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/store"
)

func TestProvisionIntegrationIdentity(t *testing.T) {
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	db, err := store.New(dsn, zap.NewNop(), 3, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			nickname := fmt.Sprintf("provision-integration-%d", time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = db.DB().ExecContext(context.Background(), "DELETE FROM users WHERE nickname = $1", nickname)
			})
			if err := run(t.Context(), nickname, "integration-password", false, enabled, dsn, time.Minute); err != nil {
				t.Fatal(err)
			}
			var got, bot bool
			read := func() {
				t.Helper()
				if err := db.DB().QueryRowContext(t.Context(), "SELECT integration_enabled, is_bot FROM users WHERE nickname = $1", nickname).Scan(&got, &bot); err != nil {
					t.Fatal(err)
				}
				if got != enabled || bot {
					t.Fatalf("unexpected identity metadata: integration=%v bot=%v", got, bot)
				}
			}
			read()
			if err := run(t.Context(), nickname, "different-password", true, !enabled, dsn, time.Minute); !errors.Is(err, auth.ErrUserExists) {
				t.Fatalf("duplicate provisioning: %v", err)
			}
			read()
		})
	}
}
