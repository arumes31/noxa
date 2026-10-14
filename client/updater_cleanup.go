package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func sameUpdatePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func updaterOwnedBackup(target, previous string) bool {
	name := filepath.Base(previous)
	return filepath.IsAbs(target) && filepath.IsAbs(previous) && !sameUpdatePath(target, previous) &&
		sameUpdatePath(filepath.Dir(target), filepath.Dir(previous)) && strings.HasPrefix(name, ".noxa-previous-") && strings.HasSuffix(name, ".exe")
}

// No directory sweep: this is one exact backup from the authenticated update
// transaction. Older, unrelated backups may still belong to another live client.
type verifiedUpdateBackup struct {
	path     string
	identity os.FileInfo
	hash     [sha256.Size]byte
}

func captureUpdateBackup(target, previous string, expected [sha256.Size]byte) (*verifiedUpdateBackup, error) {
	if !updaterOwnedBackup(target, previous) {
		return nil, fmt.Errorf("invalid updater-owned backup path")
	}
	root, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(previous)
	identity, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	backup := &verifiedUpdateBackup{path: previous, identity: identity, hash: expected}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if err := backup.verify(file); err != nil {
		return nil, err
	}
	return backup, nil
}

func (b *verifiedUpdateBackup) verify(file *os.File) error {
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if b.identity == nil || !b.identity.Mode().IsRegular() || !actual.Mode().IsRegular() || !os.SameFile(b.identity, actual) {
		return fmt.Errorf("update backup identity changed")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if [sha256.Size]byte(hash.Sum(nil)) != b.hash {
		return fmt.Errorf("update backup contents changed")
	}
	return nil
}

func (b *verifiedUpdateBackup) remove(target string, expected [sha256.Size]byte) error {
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		installed, err := hashUpdateExecutable(target)
		if err != nil || installed != expected {
			return fmt.Errorf("replacement changed before backup cleanup")
		}
		err = removeUpdateBackupFile(b)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if attempt >= 19 || !retryableUpdateCleanup(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(min(100*time.Millisecond, time.Until(deadline)))
		if !time.Now().Before(deadline) {
			return err
		}
	}
}

// Called only after the real frontend acknowledgement and startup stability
// interval. Cleanup failure must not invalidate a healthy update or trigger a
// second application launch; keep the verified backup and report the failure.
func finishUpdateBackupCleanup(target string, expected [sha256.Size]byte, backup *verifiedUpdateBackup, captureErr error) {
	err := captureErr
	if err == nil && backup == nil {
		err = fmt.Errorf("backup ownership unavailable")
	}
	if err == nil {
		err = backup.remove(target, expected)
	}
	if err != nil {
		log.Printf("update succeeded; previous executable retained after cleanup failure: %v", err)
	}
}
