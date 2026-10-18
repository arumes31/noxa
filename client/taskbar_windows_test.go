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
	keys := []trayIconKey{}
	for state := range trayStateCount {
		keys = append(keys, trayIconKey{state: state})
	}
	keys = append(keys, trayIconKey{state: trayIdle, unread: 3}, trayIconKey{state: trayMicMuted, unread: trayUnreadMax})
	for _, mode := range keys {
		if err := setTaskbarWindowIcon(hwnd, mode); err != nil {
			t.Fatal(err)
		}
		pump()
		big, _, _ := getIcon.Call(hwnd, 0x7f, 1, 0)
		small, _, _ := getIcon.Call(hwnd, 0x7f, 0, 0)
		if big == 0 || small == 0 || seen[big] {
			t.Fatalf("state %+v did not install distinct small/large icons: %d/%d", mode, small, big)
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
	for _, state := range []trayIconState{trayTalking, trayMicMuted, trayBothMuted} {
		if err := setTaskbarWindowIcon(hwnd, trayIconKey{state: state, unread: 2}); err != nil {
			t.Fatal(err)
		}
	}
	pump()
	icons, err := taskbarWindowIcons(trayIconKey{state: trayBothMuted, unread: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := getIcon.Call(hwnd, 0x7f, 1, 0)
	if got != icons[1] {
		t.Fatal("busy window lost the latest mute state")
	}
	overlays := map[uintptr]bool{}
	for state := range trayStateCount {
		handle, err := taskbarOverlayIcon(state)
		if err != nil || handle == 0 || overlays[handle] {
			t.Fatalf("state %d: overlay icon %d: %v", state, handle, err)
		}
		overlays[handle] = true
		if again, _ := taskbarOverlayIcon(state); again != handle {
			t.Fatal("overlay icon was recreated")
		}
	}
	if _, err := taskbarOverlayIcon(trayStateCount); err == nil {
		t.Fatal("invalid overlay state accepted")
	}
}
