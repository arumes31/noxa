package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testLoginPasswordStore(t *testing.T) loginPasswordStore {
	t.Helper()
	return loginPasswordStore{dir: t.TempDir(), available: true,
		protect:   func(b []byte) ([]byte, error) { return []byte(base64.StdEncoding.EncodeToString(b)), nil },
		unprotect: func(b []byte) ([]byte, error) { return base64.StdEncoding.DecodeString(string(b)) },
	}
}

func TestLoginPasswordsAreProtectedScopedAndRemovable(t *testing.T) {
	s := testLoginPasswordStore(t)
	first := loginPasswords{Password: "account-secret", ServerPassword: "server-secret"}
	if err := s.save("one:12333", "Alice", first); err != nil {
		t.Fatal(err)
	}
	got, err := s.load("one:12333", "Alice")
	if err != nil || got != first {
		t.Fatalf("round trip = %#v, %v", got, err)
	}
	for _, scope := range [][2]string{{"two:12333", "Alice"}, {"one:12333", "Bob"}} {
		got, err = s.load(scope[0], scope[1])
		if err != nil || got != (loginPasswords{}) {
			t.Fatalf("foreign connection returned credentials: %v", err)
		}
	}
	files, err := os.ReadDir(s.dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("stored files: %v, %v", files, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, files[0].Name()))
	if err != nil || bytes.Contains(raw, []byte(first.Password)) || bytes.Contains(raw, []byte(first.ServerPassword)) {
		t.Fatal("passwords written without protection")
	}
	if err := s.remove("two:12333", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.remove("one:12333", "Alice"); err != nil {
		t.Fatal(err)
	}
	got, err = s.load("one:12333", "Alice")
	if err != nil || got != (loginPasswords{}) {
		t.Fatal("forgotten credentials remain")
	}
}

func TestLoginPasswordProtectionFailurePreservesPreviousRecord(t *testing.T) {
	s := testLoginPasswordStore(t)
	old := loginPasswords{Password: "old-secret"}
	if err := s.save("server", "Alice", old); err != nil {
		t.Fatal(err)
	}
	s.protect = func([]byte) ([]byte, error) { return nil, errors.New("unavailable") }
	if err := s.save("server", "Alice", loginPasswords{Password: "new-secret"}); err == nil {
		t.Fatal("failed protection accepted")
	}
	got, err := s.load("server", "Alice")
	if err != nil || got != old {
		t.Fatal("old protected record was lost")
	}
	s.unprotect = func([]byte) ([]byte, error) { return nil, errors.New("different OS account") }
	if _, err := s.load("server", "Alice"); err == nil {
		t.Fatal("unreadable record treated as an empty guest login")
	}
}

func TestLoginPasswordStoreRefusesUnprotectedFallbackAndSwappedRecords(t *testing.T) {
	s := testLoginPasswordStore(t)
	s.available = false
	if err := s.save("server", "Alice", loginPasswords{Password: "secret"}); err == nil {
		t.Fatal("unprotected storage accepted")
	}
	s.available = true
	if err := s.save("server", "Alice", loginPasswords{Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path("server", "Alice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("server", "Bob"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load("server", "Bob"); err == nil {
		t.Fatal("credentials accepted for a different account")
	}
}

func TestRecentConnectionsPreserveExplicitAndEmptyDisplayNames(t *testing.T) {
	a := &App{settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	a.recordRecentConnection("one", "Alice", "Public Alice")
	a.recordRecentConnection("two", "Bob", "")
	a.recordRecentConnection("one", "Alice", "")
	s := a.GetSettings()
	if len(s.Recents) != 2 || s.Recents[0].Addr != "one" || s.Recents[0].DisplayName != "" || s.Recents[1].Nickname != "Bob" {
		t.Fatalf("unexpected recents: %+v", s.Recents)
	}
}
