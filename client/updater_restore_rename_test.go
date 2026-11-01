package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClientUpdateRecoveryMoveDoesNotReplaceExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "source.exe"), filepath.Join(dir, "destination.exe")
	if err := os.WriteFile(source, []byte("owned"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("external"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Call the filesystem primitive directly, bypassing the existence check to
	// model a destination appearing in the check/move gap.
	if err := renameClientUpdateRecoveryNoReplace(source, destination); err == nil {
		t.Fatal("existing destination replaced")
	}
	for path, expected := range map[string]string{source: "owned", destination: "external"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != expected {
			t.Fatalf("file changed at %s: %v", path, err)
		}
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := renameClientUpdateRecoveryNoReplace(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source was not moved: %v", err)
	}
}

func TestClientUpdateRecoveryCompletionRejectsUnverifiedRestoredImage(t *testing.T) {
	dir := t.TempDir()
	target, failed := filepath.Join(dir, "client.exe"), filepath.Join(dir, "failed.exe")
	if err := os.WriteFile(target, []byte("unexpected replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failed, []byte("retained failed image"), 0o700); err != nil {
		t.Fatal(err)
	}
	pending := &appliedClientUpdate{target: target, oldHash: sha256.Sum256([]byte("verified original"))}
	original := clientUpdateRecovery.pending
	clientUpdateRecovery.pending = pending
	t.Cleanup(func() { clientUpdateRecovery.pending = original })
	err := completeClientUpdateRecovery(pending, failed, errors.New("startup failed"))
	if err == nil || errors.Is(err, errUpdateRestored) || clientUpdateRecovery.pending != pending {
		t.Fatalf("uncertain restoration accepted: %v", err)
	}
	if data, err := os.ReadFile(failed); err != nil || string(data) != "retained failed image" {
		t.Fatal("failed image was removed before restored verification")
	}
}
