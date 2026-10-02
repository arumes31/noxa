package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyIdentityProtectionPreservesKeysAndBackupMetadata(t *testing.T) {
	a := identityTestApp(t, "off")
	first := a.ListIdentities()[0]
	if err := a.CreateIdentity("Work"); err != "" {
		t.Fatal(err)
	}
	before, err := loadIdentityAtStrict(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt = 1234567890
	if err := saveIdentityAt(first.Path, before); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "off"} {
		a.settings.IdentityKeyProtection = mode
		if err := a.ApplyIdentityProtection(); err != nil {
			t.Fatal(err)
		}
		for _, entry := range a.ListIdentities() {
			protected := entry.Protection == protectionDPAPI
			if protected != (mode == "auto" && keyProtectionAvailable()) {
				t.Fatalf("unexpected protection for %s in mode %s", entry.ID, mode)
			}
		}
		after, err := loadIdentityAtStrict(first.Path)
		if err != nil {
			t.Fatal(err)
		}
		if after.PrivateKey != before.PrivateKey || after.X25519Private != before.X25519Private || after.ExportedAt != before.ExportedAt {
			t.Fatal("applying protection changed keys or backup metadata")
		}
	}
}

func TestApplyIdentityProtectionReportsUnreadableFilesWithoutReplacingThem(t *testing.T) {
	a := identityTestApp(t, "off")
	entry := a.ListIdentities()[0]
	broken := []byte("not an identity")
	if err := os.WriteFile(entry.Path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyIdentityProtection(); err == nil {
		t.Fatal("unreadable identity was reported as protected")
	}
	after, err := os.ReadFile(entry.Path)
	if err != nil || string(after) != string(broken) {
		t.Fatal("unreadable identity was replaced")
	}
}

func TestResetIdentityRequiresConfirmedKeyAndPreservesLocalLabel(t *testing.T) {
	a := identityTestApp(t, "off")
	entry := a.ListIdentities()[0]
	if err := a.RenameIdentity(entry.ID, "Personal"); err != "" {
		t.Fatal(err)
	}
	if err := a.ResetIdentity(entry.ID, "stale-key"); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	if a.ListIdentities()[0].UniqueID != entry.UniqueID {
		t.Fatal("stale confirmation changed the key")
	}
	if err := a.ResetIdentity(entry.ID, entry.UniqueID); err != nil {
		t.Fatal(err)
	}
	after := a.ListIdentities()[0]
	if after.UniqueID == entry.UniqueID || after.Name != "Personal" || after.ExportedAt != "" {
		t.Fatal("reset did not replace the key, preserve the label, and clear backup status")
	}
	if err := a.ResetIdentity("../outside", entry.UniqueID); err == nil {
		t.Fatal("invalid identity path accepted")
	}
}

func TestCancelIdentityLevelDoesNotWaitForStoreLock(t *testing.T) {
	a := identityTestApp(t, "off")
	entry := a.ListIdentities()[0]
	a.identityMu.Lock()
	result := make(chan IdentityLevelResult, 1)
	go func() { result <- a.ImproveIdentityLevel(entry.ID, 40, 30) }()
	deadline := time.Now().Add(2 * time.Second)
	cancelled := false
	for time.Now().Before(deadline) {
		if a.CancelIdentityLevel(entry.ID) {
			cancelled = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	a.identityMu.Unlock()
	if !cancelled {
		t.Fatal("cancellation blocked behind the identity store")
	}
	select {
	case got := <-result:
		if got.Error != "" || !got.Cancelled {
			t.Fatalf("unexpected result: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled calculation did not finish")
	}
	if a.ListIdentities()[0].UniqueID != entry.UniqueID {
		t.Fatal("calculation changed the identity")
	}
	if a.CancelIdentityLevel(entry.ID) {
		t.Fatal("finished calculation retained a cancellation handle")
	}
	if got := a.ImproveIdentityLevel(entry.ID, 41, 1); !strings.Contains(got.Error, "1..40") {
		t.Fatal("invalid level accepted")
	}
}
