package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrFreshDatabaseRequired rejects databases from earlier releases. The new
// roles-v1 version starts with an empty PostgreSQL database; it does not
// translate or preserve old authorization data.
var ErrFreshDatabaseRequired = errors.New("roles-v1 requires a fresh PostgreSQL database")

const installGeneration = "roles-v1"

// EnsureFreshInstall claims an empty database for this version, or verifies a
// prior claim. The caller must hold the process lease through schema setup.
// A failed first setup can be retried against the same claimed database.
func (s *Store) EnsureFreshInstall(ctx context.Context) (retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, err)
		}
	}()
	var marker, existingTables bool
	if err := tx.QueryRowContext(ctx, `SELECT
		pg_catalog.to_regclass('public.noxa_install_generation') IS NOT NULL,
		EXISTS (SELECT 1 FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p'))`).Scan(&marker, &existingTables); err != nil {
		return fmt.Errorf("checking install generation: %w", err)
	}
	if marker {
		var generation string
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM public.noxa_install_generation`).Scan(&generation); err != nil {
			return fmt.Errorf("reading install generation: %w", err)
		}
		if generation != installGeneration {
			return ErrFreshDatabaseRequired
		}
		return tx.Commit()
	}
	if existingTables {
		return ErrFreshDatabaseRequired
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE public.noxa_install_generation (
		generation TEXT PRIMARY KEY CHECK (generation = 'roles-v1')
	)`); err != nil {
		return fmt.Errorf("creating install generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO public.noxa_install_generation (generation) VALUES ($1)`, installGeneration); err != nil {
		return fmt.Errorf("recording install generation: %w", err)
	}
	return tx.Commit()
}

// CheckFreshInstall verifies the new-version marker without writing. Operator
// activation must not silently initialize an old or unrelated database.
func (s *Store) CheckFreshInstall(ctx context.Context) error {
	var marker bool
	if err := s.db.QueryRowContext(ctx, `SELECT pg_catalog.to_regclass('public.noxa_install_generation') IS NOT NULL`).Scan(&marker); err != nil {
		return fmt.Errorf("checking install generation: %w", err)
	}
	if !marker {
		return ErrFreshDatabaseRequired
	}
	var generation string
	err := s.db.QueryRowContext(ctx, `SELECT generation FROM public.noxa_install_generation`).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFreshDatabaseRequired
	}
	if err != nil {
		return fmt.Errorf("checking install generation: %w", err)
	}
	if generation != installGeneration {
		return ErrFreshDatabaseRequired
	}
	return nil
}
