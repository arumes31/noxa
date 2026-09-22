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

	"noxa/internal/config"
	"noxa/internal/logging"
	"noxa/internal/store"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	timeout := flag.Duration("timeout", 5*time.Minute,
		"maximum time to wait for the migration lock and apply migrations")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "noxa-migrate: %v\n", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, timeout time.Duration) (runErr error) {
	if ctx == nil {
		return errors.New("migration context is nil")
	}
	if timeout <= 0 {
		return fmt.Errorf("migration timeout must be positive, got %s", timeout)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger, err := logging.New(cfg.DevMode, cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}
	defer func() {
		if err := logger.Sync(); err != nil {
			// Sync commonly returns EINVAL for console streams even though all
			// bytes were written. Surface it without turning a successful schema
			// migration into a failed command.
			fmt.Fprintf(os.Stderr, "noxa-migrate: syncing logger: %v\n", err)
		}
	}()

	logger.Info("initializing fresh-version schema", zap.String("database_url", cfg.RedactedDatabaseURL()))
	lease, err := store.AcquireRoleProcessLease(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("acquiring offline role process lease: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, lease.Close()) }()

	s, err := store.New(cfg.DatabaseURL, logger,
		cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, cfg.DBConnMaxLifetime)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("closing store: %w", err))
		}
	}()

	migrationCtx, cancelMigration := context.WithTimeout(ctx, timeout)
	if err := lease.Check(migrationCtx); err != nil {
		cancelMigration()
		return fmt.Errorf("checking offline role process lease before migration: %w", err)
	}
	err = s.EnsureFreshInstall(migrationCtx)
	if err == nil {
		err = s.MigrateContext(migrationCtx)
	}
	if err == nil {
		err = lease.Check(migrationCtx)
	}
	cancelMigration()
	if err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}

	logger.Info("fresh-version schema ready")
	return nil
}
