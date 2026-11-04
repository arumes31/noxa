//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestClientUpdateRecoveryRenameSharingLock(t *testing.T) {
	for _, scenario := range []string{"temporary", "persistent", "read temporary", "read persistent", "source changed", "destination appeared"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			source, destination := filepath.Join(dir, "previous.exe"), filepath.Join(dir, "client.exe")
			original := []byte("verified previous executable")
			if err := os.WriteFile(source, original, 0o700); err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(source)
			if err != nil {
				t.Fatal(err)
			}
			access, sharing := uint32(windows.GENERIC_READ), uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
			if strings.HasPrefix(scenario, "read ") {
				access, sharing = windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE
			}
			handle, err := windows.CreateFile(name, access, sharing, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Confirm this is a genuine Windows sharing lock, not an injected failure.
			if err := os.Rename(source, destination); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				_ = windows.CloseHandle(handle)
				t.Fatalf("missing sharing lock: %v", err)
			}
			if strings.HasPrefix(scenario, "read ") {
				if _, err := hashUpdateExecutable(source); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
					_ = windows.CloseHandle(handle)
					t.Fatalf("missing read lock: %v", err)
				}
			}
			done := make(chan error, 1)
			if !strings.Contains(scenario, "persistent") {
				go func() {
					time.Sleep(150 * time.Millisecond)
					var changeErr error
					if scenario == "source changed" {
						changeErr = os.WriteFile(source, []byte("external change"), 0o700)
					}
					if scenario == "destination appeared" {
						changeErr = os.WriteFile(destination, []byte("external destination"), 0o700)
					}
					closeErr := windows.CloseHandle(handle)
					done <- errors.Join(changeErr, closeErr)
				}()
			}
			started := time.Now()
			err = renameClientUpdateForRecovery(source, destination, sha256.Sum256(original))
			elapsed := time.Since(started)
			if strings.Contains(scenario, "persistent") {
				done <- windows.CloseHandle(handle)
			}
			if releaseErr := <-done; releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if elapsed > 10*time.Second {
				t.Fatalf("unbounded retry: %v", elapsed)
			}
			if strings.HasSuffix(scenario, "temporary") {
				if err != nil {
					t.Fatalf("temporary lock did not recover: %v", err)
				}
				if data, err := os.ReadFile(destination); err != nil || string(data) != string(original) {
					t.Fatalf("previous image not restored: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if strings.Contains(scenario, "persistent") && (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) || elapsed < 1900*time.Millisecond) {
				t.Fatalf("persistent lock budget: %v, %v", elapsed, err)
			}
			want := string(original)
			if scenario == "source changed" {
				want = "external change"
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != want {
				t.Fatalf("retained source changed: %v", err)
			}
			if scenario == "destination appeared" {
				if data, err := os.ReadFile(destination); err != nil || string(data) != "external destination" {
					t.Fatalf("external destination overwritten: %v", err)
				}
			} else if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("unexpected recovery target: %v", err)
			}
		})
	}
}

func TestClientUpdateRecoveryRejectsSameBytesFromDifferentFile(t *testing.T) {
	dir := t.TempDir()
	source, sibling, destination := filepath.Join(dir, "source.exe"), filepath.Join(dir, "sibling.exe"), filepath.Join(dir, "target.exe")
	data := []byte("same verified bytes, different identity")
	for _, path := range []string{source, sibling} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	name, err := windows.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal(err)
	}
	// Deny reads but allow moving this exact old file while the lock remains:
	// the helper cannot race ahead and move it before the replacement is ready.
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hashUpdateExecutable(source); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		_ = windows.CloseHandle(handle)
		t.Fatalf("missing read lock: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		first := os.Rename(source, source+".retained")
		var second error
		if first == nil {
			second = os.Rename(sibling, source)
		}
		done <- errors.Join(first, second, windows.CloseHandle(handle))
	}()
	err = renameClientUpdateForRecovery(source, destination, sha256.Sum256(data))
	if changeErr := <-done; changeErr != nil {
		t.Fatal(changeErr)
	}
	if err == nil {
		t.Fatal("same-content replacement passed identity guard")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("replacement moved: %v", err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != string(data) {
		t.Fatalf("external replacement changed: %v", err)
	}
}

func TestClientUpdateRecoveryWaitsForBackupReadLock(t *testing.T) {
	dir := t.TempDir()
	target, previous := filepath.Join(dir, "client.exe"), filepath.Join(dir, "previous.exe")
	old, newImage := []byte("verified previous"), []byte("failed update")
	if err := os.WriteFile(target, newImage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous, old, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(previous)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hashUpdateExecutable(previous); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		_ = windows.CloseHandle(handle)
		t.Fatalf("missing backup read lock: %v", err)
	}
	pending := &appliedClientUpdate{target: target, previous: previous, oldHash: sha256.Sum256(old), newHash: sha256.Sum256(newImage)}
	original := clientUpdateRecovery.pending
	clientUpdateRecovery.pending = pending
	t.Cleanup(func() { clientUpdateRecovery.pending = original })
	done := make(chan error, 1)
	go func() { time.Sleep(150 * time.Millisecond); done <- windows.CloseHandle(handle) }()
	err = restoreFailedClientUpdate(pending, errors.New("owned update exited"))
	if releaseErr := <-done; releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if !errors.Is(err, errUpdateRestored) || clientUpdateRecovery.pending != nil {
		t.Fatalf("backup read lock prevented recovery: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(old) {
		t.Fatalf("previous image not restored: %v", err)
	}
}

func TestClientUpdateRecoveryMoveLongPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 280 {
		dir = filepath.Join(dir, "long-recovery-directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source, destination := filepath.Join(dir, "source.exe"), filepath.Join(dir, "restored.exe")
	data := []byte("verified long-path image")
	if err := os.WriteFile(source, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := renameClientUpdateForRecovery(source, destination, sha256.Sum256(data)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != string(data) {
		t.Fatalf("long-path move failed: %v", err)
	}
}
