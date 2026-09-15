package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyConfigPreservesProfile(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "voicx")
	if err := os.MkdirAll(filepath.Join(legacy, "identities"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "known_servers.json", "identities/key.json"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyConfig(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "known_servers.json", "identities/key.json"} {
		data, err := os.ReadFile(filepath.Join(base, "noxa", name))
		if err != nil || string(data) != name {
			t.Fatalf("profile file %s changed: %q, %v", name, data, err)
		}
	}
	if err := migrateLegacyConfig(base); err != nil {
		t.Fatalf("repeat startup: %v", err)
	}
}

func TestMigrateLegacyConfigKeepsExistingProfile(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"voicx", "noxa"} {
		if err := os.Mkdir(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name, "identity.json"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyConfig(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"voicx", "noxa"} {
		data, err := os.ReadFile(filepath.Join(base, name, "identity.json"))
		if err != nil || string(data) != name {
			t.Fatalf("existing %s identity changed: %q, %v", name, data, err)
		}
	}
}

func TestMigrateLegacyConfigFreshInstall(t *testing.T) {
	if err := migrateLegacyConfig(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyConfigRejectsFile(t *testing.T) {
	for _, name := range []string{"voicx", "noxa"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, name)
			if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := migrateLegacyConfig(base); err == nil {
				t.Fatal("startup must stop when the profile path is a file")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "keep" {
				t.Fatalf("profile path changed: %q, %v", data, err)
			}
		})
	}
}
