package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityPartialEncryptionCorruptionDoesNotReplaceKeys(t *testing.T) {
	for _, missing := range []string{"public", "private"} {
		t.Run(missing, func(t *testing.T) {
			identityTestApp(t, "off")
			id, err := newIdentity("existing")
			if err != nil {
				t.Fatal(err)
			}
			if missing == "public" {
				id.X25519Public = ""
			} else {
				id.X25519Private = ""
			}
			raw, err := json.Marshal(id)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "identity.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOrCreateIdentityAt(path); err == nil {
				t.Error("partial encryption key pair silently regenerated")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("partially corrupted identity was overwritten")
			}
		})
	}
}

func TestIdentityLegacyWithoutEncryptionPairKeepsSigningKeys(t *testing.T) {
	identityTestApp(t, "off")
	id, err := newIdentity("legacy")
	if err != nil {
		t.Fatal(err)
	}
	id.X25519Public, id.X25519Private = "", ""
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateIdentityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateKey != id.PrivateKey || loaded.PublicKey != id.PublicKey {
		t.Fatal("legacy upgrade changed existing signing keys")
	}
	if _, _, err := loaded.x25519(); err != nil {
		t.Fatalf("legacy upgrade did not add a valid encryption pair: %v", err)
	}
}
