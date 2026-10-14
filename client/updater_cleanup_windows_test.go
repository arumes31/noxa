//go:build windows

package main

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestUpdateSuccessCleanupRetriesTransientWindowsLock(t *testing.T) {
	target, previous, backup, hash := updateCleanupFixture(t)
	path, err := windows.UTF16PtrFromString(previous)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(150 * time.Millisecond); _ = windows.CloseHandle(handle); close(released) }()
	defer func() { <-released }()
	if err := backup.remove(target, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("backup retained after lock release: %v", err)
	}
}

func TestUpdateSuccessCleanupPermanentLockIsBounded(t *testing.T) {
	target, previous, backup, hash := updateCleanupFixture(t)
	path, err := windows.UTF16PtrFromString(previous)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	started := time.Now()
	if err := backup.remove(target, hash); err == nil {
		t.Fatal("locked backup claimed cleaned")
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("cleanup exceeded bounded retry")
	}
	if _, err := os.Stat(previous); err != nil {
		t.Fatalf("locked backup lost: %v", err)
	}
}
