package main

import (
	"context"
	"noxa/internal/auth"
	"strings"
	"testing"
)

func TestRunRejectsInvalidMigrationBounds(t *testing.T) {
	if err := run(context.Background(), "name", "password", false, false, nil, "postgres://unused", 0); err == nil ||
		!strings.Contains(err.Error(), "positive") {
		t.Fatalf("run(zero timeout) error = %v, want timeout validation", err)
	}
}

func TestDatabaseDSNPrecedence(t *testing.T) {
	t.Setenv("NOXA_DATABASE_URL", "postgres://environment")

	got, err := databaseDSN("postgres://explicit")
	if err != nil {
		t.Fatalf("databaseDSN: %v", err)
	}
	if got != "postgres://explicit" {
		t.Fatalf("databaseDSN = %q, want explicit DSN", got)
	}

	got, err = databaseDSN("")
	if err != nil {
		t.Fatalf("databaseDSN from environment: %v", err)
	}
	if got != "postgres://environment" {
		t.Fatalf("databaseDSN = %q, want environment DSN", got)
	}
}

func TestDatabaseDSNRequired(t *testing.T) {
	t.Setenv("NOXA_DATABASE_URL", "")

	_, err := databaseDSN("")
	if err == nil || !strings.Contains(err.Error(), "NOXA_DATABASE_URL") {
		t.Fatalf("databaseDSN error = %v, want configuration error", err)
	}
}

func TestPasswordStdinPreservesSpacesAndRejectsExcessInput(t *testing.T) {
	for _, input := range []string{" strong secret ", " strong secret \n", " strong secret \r\n"} {
		got, err := readPassword(strings.NewReader(input))
		if err != nil || got != " strong secret " {
			t.Fatalf("stdin password modified: %v", err)
		}
	}
	for _, input := range []string{"", "\n", "first\nsecond", strings.Repeat("x", auth.MaxPasswordBytes+1)} {
		if _, err := readPassword(strings.NewReader(input)); err == nil {
			t.Fatal("invalid password input accepted")
		}
	}
	if _, err := readPassword(strings.NewReader(strings.Repeat("x", auth.MaxPasswordBytes) + "\r\n")); err != nil {
		t.Fatal(err)
	}
}

func TestParseScopes(t *testing.T) {
	if got, err := parseScopes("", false); err != nil || got != nil {
		t.Fatalf("unset = %v, %v", got, err)
	}
	got, err := parseScopes(" events:read , messages:manage", true)
	if err != nil || len(got) != 2 || got[0] != "events:read" {
		t.Fatalf("list = %v, %v", got, err)
	}
	if _, err := parseScopes("events:read", false); err == nil {
		t.Fatal("scopes accepted without -integration")
	}
	if _, err := parseScopes("events:read,bogus", true); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
