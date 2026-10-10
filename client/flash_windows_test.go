//go:build windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestFindMainWindowDoesNotExhaustCallbacks(t *testing.T) {
	const childFlag = "NOXA_WINDOW_LOOKUP_REGRESSION"
	if os.Getenv(childFlag) == "1" {
		// Go's Windows callback table holds 2,000 entries. A single hidden
		// startup previously filled it with a fresh closure on every lookup.
		for range 4_000 {
			findMainWindow()
		}
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFindMainWindowDoesNotExhaustCallbacks$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), childFlag+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		firstLine, _, _ := strings.Cut(string(output), "\n")
		t.Fatalf("4,000 native window lookups failed: %v: %s", err, firstLine)
	}
}

func TestWindowLookupHandlesHiddenVisibleAndOverlayWindows(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, destroy := createLookupTestWindow(t, "wailsWindow")
	defer destroy()
	_, destroyHelper := createLookupTestWindow(t, "STATIC") // A hidden tray/dialog helper is not the main window.
	defer destroyHelper()
	if got := findMainWindow(); got != 0 {
		t.Fatalf("visible lookup selected hidden window: %d", got)
	}
	if got := findOpacityWindow(); got != hwnd {
		t.Fatalf("opacity lookup = %d, want hidden Wails window %d", got, hwnd)
	}
	_, _, _ = user32.NewProc("ShowWindow").Call(hwnd, 4) // SW_SHOWNOACTIVATE
	if got := findMainWindow(); got != hwnd {
		t.Fatalf("visible lookup = %d, want Wails window %d", got, hwnd)
	}
	overlayWindows.Store(hwnd, &nativeOverlay{})
	t.Cleanup(func() { overlayWindows.Delete(hwnd) })
	if got := findOpacityWindow(); got != 0 {
		t.Fatalf("main-window lookup selected gaming overlay %d", got)
	}
}

func TestWindowLookupKeepsConcurrentRequestsSeparate(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, destroy := createLookupTestWindow(t, "NoXaWindowLookupTest")
	defer destroy()
	pid := uint32(os.Getpid())
	var group sync.WaitGroup
	for worker := range 32 {
		group.Go(func() {
			for range 64 {
				class, want := "NoXaWindowLookupTest", hwnd
				if worker%2 != 0 {
					class, want = "noXa-class-that-does-not-exist", 0
				}
				if got := findWindowForProcess(pid, class, true); got != want {
					t.Errorf("class %s lookup = %d, want %d", class, got, want)
					return
				}
			}
		})
	}
	group.Wait()
	windowSearches.Range(func(_, _ any) bool {
		t.Error("completed native window lookup retained its request")
		return false
	})
}

func lookupTestWindowProcedure(hwnd, message, wparam, lparam uintptr) uintptr {
	result, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, message, wparam, lparam)
	return result
}

// The caller defers destruction before unlocking its OS thread. Tests only
// create/destroy HWNDs belonging to this test process.
func createLookupTestWindow(t *testing.T, className string) (uintptr, func()) {
	t.Helper()
	classNameUTF16, err := windows.UTF16PtrFromString(className)
	if err != nil {
		t.Fatal(err)
	}
	unregister := func() {}
	if className != "STATIC" {
		class := overlayClass{Name: classNameUTF16, Procedure: syscall.NewCallback(lookupTestWindowProcedure)}
		class.Size = uint32(unsafe.Sizeof(class))
		atom, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
		runtime.KeepAlive(&class)
		if atom == 0 {
			t.Fatalf("register test window class: %v", err)
		}
		unregister = func() {
			_, _, _ = user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(classNameUTF16)), 0)
			runtime.KeepAlive(classNameUTF16)
		}
	}
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(classNameUTF16)), 0, 0, 0, 0, 32, 32, 0, 0, 0, 0)
	runtime.KeepAlive(classNameUTF16)
	if hwnd == 0 {
		unregister()
		t.Fatalf("create test window: %v", err)
	}
	return hwnd, func() {
		_, _, _ = user32.NewProc("DestroyWindow").Call(hwnd)
		unregister()
	}
}
