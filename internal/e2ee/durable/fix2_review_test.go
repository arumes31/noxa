// Adopted fix-review repros (1c-iii-a fix review P3-1, P3-2).
package durable

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A crash during bbolt init can leave only the first (valid) meta page.
func TestTruncatedStoreDoesNotBlockProbe(t *testing.T) {
	p := IdentityProtection(bytes.Repeat([]byte{4}, 32))
	ns := Namespace{"tls-sha256:" + strings.Repeat("c", 64), "p", "d"}
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, ns, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	entries, _ := os.ReadDir(dir)
	var db string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			db = filepath.Join(dir, e.Name())
		}
	}
	data, _ := os.ReadFile(db)
	if err := os.WriteFile(db, data[:os.Getpagesize()], 0600); err != nil {
		t.Fatal(err)
	}
	if err := ProbeUnlocked(dir); err != nil {
		t.Fatalf("ProbeUnlocked on a truncated (first-page-only) store: %v", err)
	}
}

// An Open failing inside the init transaction must release the shared dir lock.
func TestOpenFailureReleasesDirLock(t *testing.T) {
	p := IdentityProtection(bytes.Repeat([]byte{4}, 32))
	wrong := IdentityProtection(bytes.Repeat([]byte{5}, 32))
	ns := Namespace{"tls-sha256:" + strings.Repeat("c", 64), "p", "d"}
	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, ns, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(dir, ns, wrong, 0); err == nil {
		t.Fatal("expected corrupt")
	}
	release, err := LockForDestroy(dir)
	if errors.Is(err, ErrInUse) {
		t.Fatal("LEAK: own failed Open still holds the shared directory lock; destroy refused")
	}
	if err != nil {
		t.Fatal(err)
	}
	release()
	_ = time.Second
}
