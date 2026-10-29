package durable

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// 1c-iii-a item 6: the destroy lock refuses a held store and, on POSIX, keeps
// every other opener out until it is released (held across the rename).
func TestLockForDestroy(t *testing.T) {
	dir := t.TempDir()
	if release, err := LockForDestroy(filepath.Join(dir, "missing")); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	s, err := Open(dir, Namespace{"tls-sha256:" + strings.Repeat("c", 64), "p", "d"}, IdentityProtection(bytes.Repeat([]byte{4}, 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockForDestroy(dir); !errors.Is(err, ErrInUse) {
		_ = s.Close()
		t.Fatal("a held store must refuse the destroy lock", err)
	}
	_ = s.Close()
	release, err := LockForDestroy(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var file string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			file = filepath.Join(dir, e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		// While held, no opener gets in (the check-then-act gap is closed),
		// and the lock survives renaming the directory.
		if db, err := bolt.Open(file, 0600, &bolt.Options{Timeout: 100 * time.Millisecond}); err == nil {
			_ = db.Close()
			release()
			t.Fatal("an opener got in while the destroy lock was held")
		}
		moved := dir + ".deleted-x"
		if err := os.Rename(dir, moved); err != nil {
			release()
			t.Fatal(err)
		}
		movedFile := filepath.Join(moved, filepath.Base(file))
		if db, err := bolt.Open(movedFile, 0600, &bolt.Options{Timeout: 100 * time.Millisecond}); err == nil {
			_ = db.Close()
			release()
			t.Fatal("the lock must survive the rename")
		}
		file = movedFile
	}
	release()
	db, err := bolt.Open(file, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal("released lock must let openers in", err)
	}
	_ = db.Close()
}
