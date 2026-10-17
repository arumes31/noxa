package main

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Window icons are created lazily per (state, unread bucket) and kept for the
// process lifetime: at most trayStateCount×trayUnreadBuckets pairs, plus one
// glyph-only overlay per state. Windows still references installed icons, so
// transitions must not destroy the previous handle (which may also belong to
// Wails). The OS reclaims them at process exit.
var taskbarIconCache struct {
	sync.Mutex
	window  [trayStateCount][trayUnreadBuckets][2]uintptr
	overlay [trayStateCount]uintptr
}

var (
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
)

// taskbarCreateIcon builds an HICON from PNG resource bytes at a dimension.
func taskbarCreateIcon(resource []byte, dimension uintptr) (uintptr, error) {
	// #nosec G103 -- CreateIconFromResourceEx reads this owned resource buffer synchronously.
	handle, _, err := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&resource[0])), uintptr(len(resource)), 1, 0x30000, dimension, dimension, 0)
	runtime.KeepAlive(resource)
	if handle == 0 {
		return 0, err
	}
	return handle, nil
}

// taskbarWindowIcons returns the small/large window icons for key; the tray
// and the taskbar button share the same cached artwork, unread counter included.
func taskbarWindowIcons(key trayIconKey) ([2]uintptr, error) {
	if !key.valid() {
		return [2]uintptr{}, fmt.Errorf("unknown taskbar icon state %d/%d", key.state, key.unread)
	}
	taskbarIconCache.Lock()
	defer taskbarIconCache.Unlock()
	if handles := taskbarIconCache.window[key.state][key.unread]; handles[0] != 0 {
		return handles, nil
	}
	data := trayIconFor(key)
	var handles [2]uintptr
	for i, dimension := range taskbarWindowIconSizes() {
		// The native API requires DWORD-aligned resource bytes, unlike an
		// ICO's directory offset. Copy the chosen PNG to its own buffer.
		entry := trayICOEntry(data, dimension)
		if entry == nil {
			taskbarDestroyIcons(handles[:i])
			return [2]uintptr{}, fmt.Errorf("invalid taskbar icon resource %d", key.state)
		}
		handle, err := taskbarCreateIcon(append([]byte(nil), entry...), uintptr(dimension))
		if handle == 0 {
			// Nothing references the icons created so far: release them.
			taskbarDestroyIcons(handles[:i])
			return [2]uintptr{}, fmt.Errorf("create taskbar icon %d: %w", key.state, err)
		}
		handles[i] = handle
	}
	taskbarIconCache.window[key.state][key.unread] = handles
	return handles, nil
}

// taskbarDestroyIcons releases icons that were never installed.
func taskbarDestroyIcons(handles []uintptr) {
	for _, handle := range handles {
		_, _, _ = procDestroyIcon.Call(handle)
	}
}

// taskbarSystemMetric reads a GetSystemMetrics size, clamped to icon bounds.
func taskbarSystemMetric(index uintptr, fallback int) int {
	size, _, _ := procGetSystemMetrics.Call(index)
	if size == 0 {
		return fallback
	}
	return min(max(int(size), 16), 256)
}

// The system icon sizes are read once, at the scale of the primary display
// when the first icon is built: the cached handles stay installed on the
// window, so they are not rebuilt on a later DPI change (Windows rescales).
var (
	// taskbarOverlaySize is the system small-icon size (16px at 100% DPI).
	// The overlay glyph is rendered natively at that size instead of shrunk.
	taskbarOverlaySize = sync.OnceValue(func() int {
		return min(taskbarSystemMetric(49, 16), 64) // SM_CXSMICON
	})
	// taskbarWindowIconSizes are the ICON_SMALL and ICON_BIG dimensions.
	taskbarWindowIconSizes = sync.OnceValue(func() [2]int {
		return [2]int{taskbarOverlaySize(), taskbarSystemMetric(11, 32)} // SM_CXICON
	})
)

// taskbarOverlayIcon returns the glyph-only status disc for state. It never
// contains the logo, which Windows already shows as the button icon.
func taskbarOverlayIcon(state trayIconState) (uintptr, error) {
	if state >= trayStateCount {
		return 0, fmt.Errorf("invalid taskbar state")
	}
	taskbarIconCache.Lock()
	defer taskbarIconCache.Unlock()
	if handle := taskbarIconCache.overlay[state]; handle != 0 {
		return handle, nil
	}
	size := taskbarOverlaySize()
	var out bytes.Buffer
	if err := png.Encode(&out, trayOverlayImage(state, size)); err != nil {
		return 0, err
	}
	handle, err := taskbarCreateIcon(out.Bytes(), uintptr(size))
	if handle == 0 {
		return 0, fmt.Errorf("create taskbar overlay %d: %w", state, err)
	}
	taskbarIconCache.overlay[state] = handle
	return handle, nil
}

// Match the Wails class and this process, including while minimized or hidden.
// A visible-window search can select a dialog or miss close-to-tray updates.
func setTaskbarIcon(key trayIconKey) error {
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
			if err := setTaskbarWindowIcon(hwnd, key); err != nil {
				return err
			}
			// The overlay is keyed by voice state only: one 16px slot cannot
			// carry both a status glyph and a legible counter.
			return queueTaskbarOverlay(hwnd, key.state)
		}
	}
}

func setTaskbarWindowIcon(hwnd uintptr, key trayIconKey) error {
	handles, err := taskbarWindowIcons(key)
	if err != nil {
		return err
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
