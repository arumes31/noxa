//go:build windows

// flash_windows.go implements the taskbar flash for whisper notifications
// using the Win32 FlashWindow API. Wails doesn't expose the window handle,
// so we find our own top-level window by process ID via EnumWindows.
package main

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"noxa/internal/safecast"
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows     = user32.NewProc("EnumWindows")
	procFlashWindow     = user32.NewProc("FlashWindow")
	procGetWindowPID    = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procGetClassName    = user32.NewProc("GetClassNameW")
	windowSearches      sync.Map // integer token -> *mainWindowSearch, only during EnumWindows
	windowSearchID      atomic.Uintptr
	// NewCallback allocations are never released by Go. Retain one callback
	// and pass an integer token, rather than allocating a captured closure or
	// passing a Go pointer through LPARAM on every lookup.
	mainWindowCallback = syscall.NewCallback(collectMainWindow)
)

type mainWindowSearch struct {
	pid           uint32
	class         string
	includeHidden bool
	hwnd          atomic.Uintptr
}

func collectMainWindow(hwnd, token uintptr) uintptr {
	value, ok := windowSearches.Load(token)
	if !ok || isGamingOverlayWindow(hwnd) {
		return 1
	}
	search := value.(*mainWindowSearch)
	var pid uint32
	// #nosec G103 -- GetWindowThreadProcessId requires a writable DWORD pointer.
	_, _, _ = procGetWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != search.pid {
		return 1
	}
	if search.class != "" {
		var class [256]uint16
		// #nosec G103 -- GetClassNameW writes a synchronous UTF-16 buffer.
		length, _, _ := procGetClassName.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		runtime.KeepAlive(&class)
		if length == 0 || windows.UTF16ToString(class[:]) != search.class {
			return 1
		}
	}
	if !search.includeHidden {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
	}
	search.hwnd.Store(hwnd)
	return 0
}

func findWindowForProcess(pid uint32, class string, includeHidden bool) uintptr {
	search := &mainWindowSearch{pid: pid, class: class, includeHidden: includeHidden}
	var token uintptr
	for {
		token = windowSearchID.Add(1)
		if token == 0 {
			continue
		}
		if _, exists := windowSearches.LoadOrStore(token, search); !exists {
			break
		}
	}
	// EnumWindows invokes callbacks synchronously. Each enumeration has its
	// own request, including concurrent notification and opacity lookups.
	defer windowSearches.Delete(token)
	_, _, _ = procEnumWindows.Call(mainWindowCallback, token)
	return search.hwnd.Load()
}

// findMainWindow returns the first visible top-level window owned by this
// process, or 0.
func findMainWindow() uintptr {
	pid, err := safecast.IntToUint32(os.Getpid())
	if err != nil {
		return 0
	}
	return findWindowForProcess(pid, "", false)
}

// findOpacityWindow also finds a hidden main window. Match Wails' native
// class, as a hidden tray helper or dialog must never receive main opacity.
func findOpacityWindow() uintptr {
	pid, err := safecast.IntToUint32(os.Getpid())
	if err != nil {
		return 0
	}
	return findWindowForProcess(pid, "wailsWindow", true)
}

// flashWindowOnce flashes the main window's taskbar button once.
func flashWindowOnce() error {
	hwnd := findMainWindow()
	if hwnd == 0 {
		return nil // headless or no window yet: nothing to flash
	}
	_, _, _ = procFlashWindow.Call(hwnd, 1)
	return nil
}
