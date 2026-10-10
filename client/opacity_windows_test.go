//go:build windows && (amd64 || arm64)

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestWindowOpacityAppliesBeforeHiddenWindowIsShown(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, destroy := createLookupTestWindow(t, "wailsWindow")
	defer destroy()
	if err := setWindowOpacity(70); err != nil {
		t.Fatal(err)
	}
	var color, flags uint32
	var alpha byte
	getAttributes := user32.NewProc("GetLayeredWindowAttributes")
	ok, _, err := getAttributes.Call(hwnd, uintptr(unsafe.Pointer(&color)), uintptr(unsafe.Pointer(&alpha)), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || alpha != 70*255/100 || flags != lwaAlpha {
		t.Fatalf("hidden-window opacity = %d/%d, status %d: %v", alpha, flags, ok, err)
	}
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible != 0 {
		t.Fatal("restoring opacity made the hidden native window visible")
	}
}
