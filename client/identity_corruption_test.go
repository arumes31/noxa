package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityEmptyExistingFileDoesNotRegenerate(t *testing.T) {
	identityTestApp(t, "off")
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := loadOrCreateIdentityAt(path); err == nil || id != nil {
		t.Error("empty existing identity silently created a replacement account")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatal("empty existing identity was overwritten")
	}
}

func TestIdentityCorruptProtectedEncryptionKeyIsPreserved(t *testing.T) {
	identityTestApp(t, "off")
	path := filepath.Join(t.TempDir(), "identity.json")
	id, err := newIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	id.X25519Private = dpapiPrefix + base64.StdEncoding.EncodeToString([]byte("invalid protected encryption key"))
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateIdentityAt(path); !errors.Is(err, errProtectedUnreadable) {
		t.Fatalf("corrupt protected encryption key error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("corrupt protected encryption identity was overwritten")
	}
}

func TestIdentityLegacyEncryptionKeyGainsProtectionOnLoad(t *testing.T) {
	if !keyProtectionAvailable() {
		t.Skip("platform key protection unavailable")
	}
	identityTestApp(t, "auto")
	path := filepath.Join(t.TempDir(), "identity.json")
	id, err := newIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	legacy := *id
	blob, err := protectBytes([]byte(id.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	legacy.PrivateKey = dpapiPrefix + base64.StdEncoding.EncodeToString(blob)
	legacy.Protection = protectionDPAPI
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateIdentityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateKey != id.PrivateKey || loaded.X25519Private != id.X25519Private {
		t.Fatal("legacy migration changed private keys")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var migrated identity
	if err := json.Unmarshal(raw, &migrated); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(migrated.X25519Private, dpapiPrefix) || strings.Contains(string(raw), id.X25519Private) {
		t.Fatal("legacy encryption private key remains plaintext after load")
	}
}

func TestIdentityProtectionIncludesEncryptionPrivateKey(t *testing.T) {
	if !keyProtectionAvailable() {
		t.Skip("platform key protection unavailable")
	}
	identityTestApp(t, "auto")
	path := filepath.Join(t.TempDir(), "identity.json")
	id, err := loadOrCreateIdentityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted identity
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(persisted.X25519Private, dpapiPrefix) || strings.Contains(string(raw), id.X25519Private) {
		t.Error("protected identity exposes its encryption private key in plaintext")
	}
	reloaded, err := loadOrCreateIdentityAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.X25519Private != id.X25519Private || reloaded.X25519Public != id.X25519Public {
		t.Fatal("protected encryption keys changed after reload")
	}
}
