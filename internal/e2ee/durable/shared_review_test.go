package durable

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSharedMissingDirectoryThroughLink(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "link")
	if e := os.Mkdir(actual, 0700); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS == "windows" {
		if b, e := exec.CommandContext(context.Background(), "cmd", "/c", "mklink", "/J", link, actual).CombinedOutput(); e != nil {
			t.Fatalf("junction: %s %v", b, e)
		}
	} else if e := os.Symlink(actual, link); e != nil {
		t.Fatal(e)
	}
	p := IdentityProtection(bytes.Repeat([]byte{7}, 32))
	a, e := OpenShared(filepath.Join(link, "new"), testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	b, e := OpenShared(filepath.Join(actual, "new"), testNS, p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = b.Close() }()
	if a.Store != b.Store {
		t.Fatal("distinct shared handles")
	}
}

func TestProbeUnlockedDetectsHeldStore(t *testing.T) {
	dir := t.TempDir()
	if e := ProbeUnlocked(filepath.Join(dir, "missing")); e != nil {
		t.Fatal(e)
	}
	s, e := Open(dir, Namespace{"tls-sha256:" + strings.Repeat("a", 64), "p", "d"}, IdentityProtection(bytes.Repeat([]byte{5}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = ProbeUnlocked(dir); !errors.Is(e, ErrInUse) {
		_ = s.Close()
		t.Fatal("a held store must be detected on every platform", e)
	}
	_ = s.Close()
	if e = ProbeUnlocked(dir); e != nil {
		t.Fatal("a released store must probe clean", e)
	}
}

// Reviewer repro (fix3 P3-2): only the lock timeout means "in use". A 0-byte
// (crash during creation), garbage or zero-filled store file is not held by
// any opener and must not block the identity deletion that destroys it.
func TestProbeUnlockedSkipsUnusableStoreFiles(t *testing.T) {
	for name, content := range map[string][]byte{
		"zero-byte": nil,
		"garbage":   []byte("garbage-not-bolt-garbage-not-bolt"),
		"zeros8k":   make([]byte, 8192),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if e := os.WriteFile(filepath.Join(dir, "a.db"), content, 0600); e != nil {
				t.Fatal(e)
			}
			if e := ProbeUnlocked(dir); e != nil {
				t.Fatalf("unusable store file must not block deletion: %v", e)
			}
		})
	}
	// A held store next to a broken one is still detected.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "broken.db"), nil, 0600)
	s, e := Open(dir, Namespace{"tls-sha256:" + strings.Repeat("b", 64), "p", "d"}, IdentityProtection(bytes.Repeat([]byte{6}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if e = ProbeUnlocked(dir); !errors.Is(e, ErrInUse) {
		t.Fatal("held store must still be detected", e)
	}
}
