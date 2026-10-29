package main

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestUpdateWindowNativePlacementRoundtrip(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, err := windows.UTF16PtrFromString("STATIC")
	if err != nil {
		t.Fatal(err)
	}
	create := func() uintptr {
		hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0xcf0000, 180, 140, 980, 700, 0, 0, 0, 0)
		if hwnd == 0 {
			t.Fatalf("create test window: %v", err)
		}
		return hwnd
	}
	source := create()
	defer func() { _, _, _ = user32.NewProc("DestroyWindow").Call(source) }()
	target := create()
	defer func() { _, _, _ = user32.NewProc("DestroyWindow").Call(target) }()
	for _, maximized := range []bool{false, true} {
		_, _, _ = user32.NewProc("ShowWindow").Call(source, map[bool]uintptr{false: 1, true: 3}[maximized])
		saved, err := captureUpdateWindowHandle(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyUpdateWindowHandle(target, *saved); err != nil {
			t.Fatal(err)
		}
		restored, err := captureUpdateWindowHandle(target)
		if err != nil {
			t.Fatal(err)
		}
		if *saved != *restored || restored.Maximized != maximized {
			t.Fatalf("placement changed: %+v -> %+v", saved, restored)
		}
	}
}
