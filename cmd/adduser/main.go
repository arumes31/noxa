// adduser registers a noxa user in the database and prints its unique ID.
//
// Usage:
//
//	adduser -nickname <name> -password <pw> [-bot] [-integration] [-db <dsn>] [-migration-timeout 5m]
//
// The database DSN comes from -db, then NOXA_DATABASE_URL. Migrations are
// applied (idempotent). The database process lease requires the server to be
// stopped so an opt-in default role cannot be assigned behind its live cache.
//
// Exit codes: 0 on success, and also 0 when the user already exists
// (idempotent for provisioning scripts); 1 on real errors.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/store"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	nickname := flag.String("nickname", "", "user nickname (required)")
	password := flag.String("password", "", "user password (required)")
	bot := flag.Bool("bot", false, "mark an account as a bot (identity only; grants no permissions)")
	integration := flag.Bool("integration", false, "allow role-mode integration login (grants no permissions)")
	dsn := flag.String("db", "", "database DSN (default: NOXA_DATABASE_URL; required when unset)")
	migrationTimeout := flag.Duration("migration-timeout", 5*time.Minute,
		"maximum time to wait for the migration lock and apply migrations")
	flag.Parse()

	if *nickname == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "adduser: -nickname and -password are required")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *nickname, *password, *bot, *integration, *dsn, *migrationTimeout); err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			fmt.Printf("user %q already exists (no changes made)\n", *nickname)
			return 0
		}
		fmt.Fprintf(os.Stderr, "adduser: %v\n", err)
		return 1
	}
	return 0
}

func run(
	ctx context.Context,
	nickname, password string,
	bot, integration bool,
	dsn string,
	migrationTimeout time.Duration,
) (retErr error) {
	if ctx == nil {
		return errors.New("command context is nil")
	}
	if migrationTimeout <= 0 {
		return fmt.Errorf("migration timeout must be positive, got %s", migrationTimeout)
	}
	dsn, err := databaseDSN(dsn)
	if err != nil {
		return err
	}
	lease, err := store.AcquireRoleProcessLease(ctx, dsn)
	if err != nil {
		return fmt.Errorf("acquiring offline role process lease: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lease.Close()) }()

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	dbStore, err := store.New(dsn, logger, 5, 1, time.Minute)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := dbStore.Close(); err != nil {
			logger.Warn("closing store failed", zap.Error(err))
		}
	}()

	migrationCtx, cancelMigration := context.WithTimeout(ctx, migrationTimeout)
	err = dbStore.EnsureFreshInstall(migrationCtx)
	if err == nil {
		err = dbStore.MigrateContext(migrationCtx)
	}
	cancelMigration()
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	authSvc := auth.New(dbStore, logger)
	if err := lease.Check(ctx); err != nil {
		return fmt.Errorf("checking offline role process lease before registration: %w", err)
	}
	uniqueID, err := authSvc.RegisterUser(ctx, nickname, password)
	if err != nil {
		return err
	}

	if bot {
		if _, err := dbStore.DB().ExecContext(ctx, `UPDATE users SET is_bot = TRUE WHERE unique_id = $1`, uniqueID); err != nil {
			return fmt.Errorf("marking bot identity: %w", err)
		}
	}

	if integration {
		if _, err := dbStore.DB().ExecContext(ctx, `UPDATE users SET integration_enabled = TRUE WHERE unique_id = $1`, uniqueID); err != nil {
			return fmt.Errorf("enabling integration identity: %w", err)
		}
	}
	if err := lease.Check(ctx); err != nil {
		return fmt.Errorf("checking offline role process lease after registration: %w", err)
	}

	fmt.Printf("registered user %q\nunique_id: %s\nbot: %v\nintegration: %v\n", nickname, uniqueID, bot, integration)
	return nil
}

func databaseDSN(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if fromEnv := os.Getenv("NOXA_DATABASE_URL"); fromEnv != "" {
		return fromEnv, nil
	}
	return "", errors.New("database DSN is required: set -db or NOXA_DATABASE_URL")
}
