package main

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTaskbarVoiceIconsChangeOnHiddenWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, err := windows.UTF16PtrFromString("STATIC")
	if err != nil {
		t.Fatal(err)
	}
	createWindow := user32.NewProc("CreateWindowExW")
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 32, 32, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("create hidden test window: %v", err)
	}
	defer func() { _, _, _ = user32.NewProc("DestroyWindow").Call(hwnd) }()
	getIcon := user32.NewProc("SendMessageW")
	pump := func() {
		// Aligned storage larger than MSG on both Windows architectures.
		var message [8]uintptr
		for {
			ok, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&message[0])), hwnd, 0, 0, 1)
			if ok == 0 {
				return
			}
			_, _, _ = user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&message[0])))
		}
	}
	seen := make(map[uintptr]bool)
	for mode := trayIdle; mode <= trayTalkingDeafened; mode++ {
		if err := setTaskbarWindowIcon(hwnd, mode); err != nil {
			t.Fatal(err)
		}
		pump()
		big, _, _ := getIcon.Call(hwnd, 0x7f, 1, 0)
		small, _, _ := getIcon.Call(hwnd, 0x7f, 0, 0)
		if big == 0 || small == 0 || seen[big] {
			t.Fatalf("state %d did not install distinct small/large icons: %d/%d", mode, small, big)
		}
		seen[big] = true
		if err := setTaskbarWindowIcon(hwnd, mode); err != nil {
			t.Fatal(err)
		}
		pump()
		again, _, _ := getIcon.Call(hwnd, 0x7f, 1, 0)
		if again != big {
			t.Fatal("repeated state allocated another native icon")
		}
	}
	// A busy UI must eventually display the newest queued state even when
	// several voice transitions arrive before it processes window messages.
	for _, mode := range []trayIconState{trayTalking, trayMicMuted, trayBothMuted} {
		if err := setTaskbarWindowIcon(hwnd, mode); err != nil {
			t.Fatal(err)
		}
	}
	pump()
	icons, err := taskbarIcons()
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := getIcon.Call(hwnd, 0x7f, 1, 0)
	if got != icons[trayBothMuted][1] {
		t.Fatal("busy window lost the latest mute state")
	}
}
