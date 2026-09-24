package store

import (
	"errors"
	"testing"
)

func TestFreshInstallClaimsEmptyDatabaseAndRestarts(t *testing.T) {
	s := testScratchStore(t)
	if err := s.EnsureFreshInstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckFreshInstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureFreshInstall(t.Context()); err != nil {
		t.Fatalf("repeat startup: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("repeat schema initialization: %v", err)
	}
}

func TestFreshInstallRejectsOldDatabaseBeforeSchemaChanges(t *testing.T) {
	s := testScratchStore(t)
	if _, err := s.DB().ExecContext(t.Context(), `CREATE TABLE users (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureFreshInstall(t.Context()); !errors.Is(err, ErrFreshDatabaseRequired) {
		t.Fatalf("existing database error = %v", err)
	}
	if err := s.CheckFreshInstall(t.Context()); !errors.Is(err, ErrFreshDatabaseRequired) {
		t.Fatalf("unmarked database error = %v", err)
	}
	var marker bool
	if err := s.DB().QueryRowContext(t.Context(), `SELECT pg_catalog.to_regclass('public.noxa_install_generation') IS NOT NULL`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker {
		t.Fatal("rejected database received an install marker")
	}
}
