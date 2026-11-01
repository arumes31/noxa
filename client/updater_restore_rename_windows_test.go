//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestClientUpdateRecoveryRenameSharingLock(t *testing.T) {
	for _, scenario := range []string{"temporary", "persistent", "source changed", "destination appeared"} {
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
			handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Confirm this is a genuine Windows sharing lock, not an injected failure.
			if err := os.Rename(source, destination); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				_ = windows.CloseHandle(handle)
				t.Fatalf("missing sharing lock: %v", err)
			}
			done := make(chan error, 1)
			if scenario != "persistent" {
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
			if scenario == "persistent" {
				done <- windows.CloseHandle(handle)
			}
			if releaseErr := <-done; releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if elapsed > 10*time.Second {
				t.Fatalf("unbounded retry: %v", elapsed)
			}
			if scenario == "temporary" {
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
			if scenario == "persistent" && (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) || elapsed < 1900*time.Millisecond) {
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
