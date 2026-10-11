//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/minio/selfupdate"
	"golang.org/x/sys/windows"
)

func lockedClientUpdateFixture(t *testing.T) (selfupdate.Options, [sha256.Size]byte, [sha256.Size]byte, func(), error) {
	t.Helper()
	dir := t.TempDir()
	options := selfupdate.Options{TargetPath: filepath.Join(dir, "client.exe"), OldSavePath: filepath.Join(dir, ".previous.exe")}
	old, replacement := []byte("original executable fixture"), []byte("verified replacement fixture")
	if err := os.WriteFile(options.TargetPath, old, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(options.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	// Model an external scanner/reader that permits reads but denies rename.
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	closeHandle := func() {
		if !closed {
			closed = true
			if err := windows.CloseHandle(handle); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(closeHandle)
	err = selfupdate.Apply(bytes.NewReader(replacement), options)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("expected first-rename sharing violation: %v", err)
	}
	return options, sha256.Sum256(old), sha256.Sum256(replacement), closeHandle, err
}

func TestClientUpdateCommitRetriesTransientPreMoveSharingViolation(t *testing.T) {
	options, oldHash, newHash, release, originalErr := lockedClientUpdateFixture(t)
	released := make(chan struct{})
	go func() { time.Sleep(100 * time.Millisecond); release(); close(released) }()
	err := retryClientUpdateCommit(originalErr, options, oldHash, newHash)
	<-released
	if err != nil {
		t.Fatalf("transient sharing violation did not recover: %v", err)
	}
	if got, err := hashUpdateExecutable(options.TargetPath); err != nil || got != newHash {
		t.Fatalf("replacement not installed: %v", err)
	}
	if got, err := hashUpdateExecutable(options.OldSavePath); err != nil || got != oldHash {
		t.Fatalf("original backup not retained: %v", err)
	}
}

func TestClientUpdateCommitPersistentLockIsBoundedAndPreservesFiles(t *testing.T) {
	options, oldHash, newHash, _, originalErr := lockedClientUpdateFixture(t)
	started := time.Now()
	err := retryClientUpdateCommit(originalErr, options, oldHash, newHash)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("persistent lock accepted: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 1900*time.Millisecond || elapsed > 10*time.Second {
		t.Fatalf("retry duration not bounded: %v", elapsed)
	}
	if got, err := hashUpdateExecutable(options.TargetPath); err != nil || got != oldHash {
		t.Fatalf("locked target changed: %v", err)
	}
	staged := filepath.Join(filepath.Dir(options.TargetPath), ".client.exe.new")
	if got, err := hashUpdateExecutable(staged); err != nil || got != newHash {
		t.Fatalf("verified staging changed: %v", err)
	}
	if _, err := os.Lstat(options.OldSavePath); !os.IsNotExist(err) {
		t.Fatal("lock created or replaced a backup")
	}
}

func TestClientUpdateCommitRejectsChangedFilesAndLaterOperations(t *testing.T) {
	for _, scenario := range []string{"target changed", "staging changed", "backup appeared", "second rename", "access denied"} {
		t.Run(scenario, func(t *testing.T) {
			options, oldHash, newHash, release, originalErr := lockedClientUpdateFixture(t)
			release()
			staged := filepath.Join(filepath.Dir(options.TargetPath), ".client.exe.new")
			changedPath := ""
			switch scenario {
			case "target changed":
				changedPath = options.TargetPath
			case "staging changed":
				changedPath = staged
			case "backup appeared":
				changedPath = options.OldSavePath
			case "second rename":
				originalErr = &os.LinkError{Op: "rename", Old: staged, New: options.TargetPath, Err: windows.ERROR_SHARING_VIOLATION}
			case "access denied":
				originalErr = &os.LinkError{Op: "rename", Old: options.TargetPath, New: options.OldSavePath, Err: windows.ERROR_ACCESS_DENIED}
			}
			if changedPath != "" {
				if err := os.WriteFile(changedPath, []byte("external change"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := retryClientUpdateCommit(originalErr, options, oldHash, newHash); err != originalErr {
				t.Fatalf("unsafe retry: %v", err)
			}
			if changedPath != "" {
				if data, err := os.ReadFile(changedPath); err != nil || string(data) != "external change" {
					t.Fatal("external change overwritten")
				}
			}
			if changedPath != options.TargetPath {
				if got, err := hashUpdateExecutable(options.TargetPath); err != nil || got != oldHash {
					t.Fatal("target changed after rejected retry")
				}
			}
		})
	}
}

func TestClientUpdateCommitDoesNotRetrySharingViolationAfterRollback(t *testing.T) {
	options, oldHash, newHash, release, _ := lockedClientUpdateFixture(t)
	release()
	staged := filepath.Join(filepath.Dir(options.TargetPath), ".client.exe.new")
	path, err := windows.UTF16PtrFromString(staged)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The first rename succeeds, the locked staged-file rename fails, and the
	// library restores the original. Hashes alone now look safe to replay.
	commitErr := selfupdate.CommitBinary(options)
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(commitErr, windows.ERROR_SHARING_VIOLATION) || selfupdate.RollbackError(commitErr) != nil {
		t.Fatalf("expected successful library rollback: %v", commitErr)
	}
	if err := retryClientUpdateCommit(commitErr, options, oldHash, newHash); err != commitErr {
		t.Fatalf("post-move operation retried: %v", err)
	}
	if got, err := hashUpdateExecutable(options.TargetPath); err != nil || got != oldHash {
		t.Fatal("rolled-back target replaced")
	}
	if got, err := hashUpdateExecutable(staged); err != nil || got != newHash {
		t.Fatal("staging changed after rollback")
	}
}
