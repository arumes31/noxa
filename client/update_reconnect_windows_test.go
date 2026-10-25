package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdateReconnectDPAPIProtectionAndChainedUpdate(t *testing.T) {
	saved := testUpdateEnvelope(t)
	encrypted, err := encodeUpdateReconnect(saved, protectBytes)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{saved.Identity.PrivateKey, saved.Passwords.Password, saved.Passwords.ServerPassword} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("handoff contains plaintext credentials")
		}
	}
	restored, err := decodeUpdateReconnect(encrypted, time.Now(), unprotectBytes)
	if err != nil || restored.Passwords != saved.Passwords || restored.Identity.PrivateKey != saved.Identity.PrivateKey {
		t.Fatalf("protected handoff lost authentication: %v", err)
	}
	previous := startedAfterUpdate
	startedAfterUpdate = true
	defer func() { startedAfterUpdate = previous }()
	t.Setenv(updateReconnectEnv, encrypted)
	a := newTabApp(t)
	if got := a.PrepareUpdateReconnect(false, false, false); got != "" || a.updateReconnect != encrypted {
		t.Fatal("chained updater dropped unconsumed reconnect")
	}
	a.updateReconnectConsumed = true
	_ = os.Unsetenv(updateReconnectEnv)
	if got := a.PrepareUpdateReconnect(false, false, false); got != "" || a.updateReconnect != "" {
		t.Fatal("consumed handoff reused")
	}
}
