package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Keep a bounded set of twelve HICONs for the process lifetime. Windows still
// references installed icons, so transitions must not destroy the previous
// handle (which may also belong to Wails). The OS reclaims them at process exit.
var taskbarIcons = sync.OnceValues(func() (map[trayIconState][2]uintptr, error) {
	icons := make(map[trayIconState][2]uintptr)
	create := user32.NewProc("CreateIconFromResourceEx")
	for mode, data := range trayIcons() {
		if len(data) < 22 {
			return nil, fmt.Errorf("invalid taskbar icon %d", mode)
		}
		size := uint64(binary.LittleEndian.Uint32(data[14:18]))
		offset := uint64(binary.LittleEndian.Uint32(data[18:22]))
		if size == 0 || offset+size > uint64(len(data)) {
			return nil, fmt.Errorf("invalid taskbar icon resource %d", mode)
		}
		// The native API requires DWORD-aligned resource bytes, unlike an ICO's
		// directory offset. Copy the PNG/DIB payload to its own aligned buffer.
		resource := append([]byte(nil), data[offset:offset+size]...)
		var handles [2]uintptr
		for i, dimension := range []uintptr{16, 32} {
			// #nosec G103 -- CreateIconFromResourceEx reads this owned resource buffer synchronously.
			handle, _, err := create.Call(uintptr(unsafe.Pointer(&resource[0])), uintptr(len(resource)), 1, 0x30000, dimension, dimension, 0)
			runtime.KeepAlive(resource)
			if handle == 0 {
				return nil, fmt.Errorf("create taskbar icon %d: %w", mode, err)
			}
			handles[i] = handle
		}
		icons[mode] = handles
	}
	return icons, nil
})

// Match the Wails class and this process, including while minimized or hidden.
// A visible-window search can select a dialog or miss close-to-tray updates.
func setTaskbarIcon(mode trayIconState) error {
	class, err := windows.UTF16PtrFromString("wailsWindow")
	if err != nil {
		return err
	}
	find := user32.NewProc("FindWindowExW")
	var hwnd uintptr
	for {
		// #nosec G103 -- FindWindowExW reads a NUL-terminated UTF-16 class name.
		hwnd, _, _ = find.Call(0, hwnd, uintptr(unsafe.Pointer(class)), 0)
		runtime.KeepAlive(class)
		if hwnd == 0 {
			return nil // Tray starts before Wails; OnDomReady reapplies the state.
		}
		var pid uint32
		// #nosec G103 -- GetWindowThreadProcessId writes the provided DWORD.
		_, _, _ = procGetWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if uint64(pid) == uint64(os.Getpid()) {
			return setTaskbarWindowIcon(hwnd, mode)
		}
	}
}

func setTaskbarWindowIcon(hwnd uintptr, mode trayIconState) error {
	icons, err := taskbarIcons()
	if err != nil {
		return err
	}
	handles, ok := icons[mode]
	if !ok {
		return fmt.Errorf("unknown taskbar icon state %d", mode)
	}
	send := user32.NewProc("PostMessageW")
	for i, handle := range handles {
		// WM_SETICON: small=0, big=1. Queue in publication order so a busy
		// window catches up instead of timing out and losing its final state.
		// Only process-lifetime handles cross the asynchronous boundary.
		ok, _, err := send.Call(hwnd, 0x80, uintptr(i), handle)
		if ok == 0 {
			return fmt.Errorf("update taskbar icon: %w", err)
		}
	}
	return nil
}
