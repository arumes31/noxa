//go:build !windows

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

// Adopted review repros (1c-iii-a2 P3-4, POSIX): the destroy lock also keeps
// out namespace stores created after it was taken, and an opener that was
// blocked behind it never opens the renamed (deleted) store afterwards.
func TestLockForDestroyBlocksLateNamespaceAndStaleOpener(t *testing.T) {
	p := IdentityProtection(bytes.Repeat([]byte{4}, 32))
	nsA := Namespace{"tls-sha256:" + strings.Repeat("c", 64), "p", "d"}
	nsB := Namespace{"tls-sha256:" + strings.Repeat("d", 64), "p", "d"}

	dir := filepath.Join(t.TempDir(), "s")
	s, err := Open(dir, nsA, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	release, err := LockForDestroy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2, err := Open(dir, nsB, p, 200*time.Millisecond); err == nil {
		_ = s2.Close()
		release()
		t.Fatal("a second-namespace store got in while the destroy lock was held")
	} else if !errors.Is(err, ErrInUse) {
		release()
		t.Fatal(err)
	}
	release()

	dir2 := filepath.Join(t.TempDir(), "s")
	s, err = Open(dir2, nsA, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	release, err = LockForDestroy(dir2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		st, e := Open(dir2, nsA, p, 2*time.Second)
		if st != nil {
			_ = st.Close()
		}
		done <- e
	}()
	time.Sleep(100 * time.Millisecond)
	if err := os.Rename(dir2, dir2+".deleted-x"); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(dir2 + ".deleted-x")
	release()
	if e := <-done; e == nil {
		t.Fatal("an opener blocked behind the destroy lock opened the deleted store")
	}
	// A held store refuses the destroy lock (shared directory lock).
	dir3 := filepath.Join(t.TempDir(), "s")
	s, err = Open(dir3, nsA, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := LockForDestroy(dir3); !errors.Is(err, ErrInUse) {
		t.Fatal("held store must refuse the destroy lock", err)
	}
}
