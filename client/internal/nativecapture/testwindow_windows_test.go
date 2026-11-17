//go:build windows

package nativecapture

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procPatBlt           = gdi32.NewProc("PatBlt")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	testWndProc          = windows.NewCallback(func(hwnd, msg, w, l uintptr) uintptr {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, w, l)
		return r
	})
	testClassRegistered atomic.Bool
)

// animatedWindow is a GDI window repainting a moving pattern as fast as it
// can (a stand-in for a game); fullscreen makes it a topmost popup over the
// primary monitor that keeps the focus.
type animatedWindow struct {
	hwnd    atomic.Uintptr
	stop    chan struct{}
	stopped chan struct{}
	paints  atomic.Uint64
}

func startAnimatedWindow(t *testing.T, fullscreen bool, w, h int) *animatedWindow {
	t.Helper()
	a := &animatedWindow{stop: make(chan struct{}), stopped: make(chan struct{})}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(a.stopped)
		class, _ := windows.UTF16PtrFromString("noxaNativeCaptureTest")
		if testClassRegistered.CompareAndSwap(false, true) {
			wc := struct {
				size, style                   uint32
				proc                          uintptr
				clsExtra, wndExtra            int32
				instance, icon, cursor, brush uintptr
				menu, class                   *uint16
				iconSm                        uintptr
			}{proc: testWndProc, class: class}
			wc.size = uint32(unsafe.Sizeof(wc))
			_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))) // #nosec G103
		}
		style, exStyle, x, y := uintptr(0x10CF0000), uintptr(0), uintptr(100), uintptr(100) // WS_OVERLAPPEDWINDOW|WS_VISIBLE
		if fullscreen {
			style, exStyle, x, y = 0x90000000, 0x8, 0, 0 // WS_POPUP|WS_VISIBLE, WS_EX_TOPMOST
		}
		title, _ := windows.UTF16PtrFromString("noXa native capture test")
		hwnd, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), style, // #nosec G103
			x, y, uintptr(w), uintptr(h), 0, 0, 0, 0)
		a.hwnd.Store(hwnd)
		_, _, _ = procShowWindow.Call(hwnd, 5)
		_, _, _ = procSetForeground.Call(hwnd)
		close(ready)
		if hwnd == 0 {
			return
		}
		var msg [48]byte
		for i := 0; ; i++ {
			select {
			case <-a.stop:
				_, _, _ = procDestroyWindow.Call(hwnd)
				return
			default:
			}
			for {
				ok, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1) // #nosec G103 -- PM_REMOVE
				if ok == 0 {
					break
				}
				_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg))) // #nosec G103
				_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg))) // #nosec G103
			}
			dc, _, _ := procGetDC.Call(hwnd)
			brush, _, _ := procCreateSolidBrush.Call(uintptr(i*7%256) | uintptr(i*3%256)<<8 | uintptr(255-i%256)<<16)
			old, _, _ := procSelectObject.Call(dc, brush)
			_, _, _ = procPatBlt.Call(dc, 0, 0, uintptr(w), uintptr(h), 0x00F00021)      // PATCOPY
			_, _, _ = procPatBlt.Call(dc, uintptr(i*8%w), 0, 64, uintptr(h), 0x00550009) // DSTINVERT bar
			_, _, _ = procSelectObject.Call(dc, old)
			_, _, _ = procDeleteObject.Call(brush)
			_, _, _ = procReleaseDC.Call(hwnd, dc)
			a.paints.Add(1)
			time.Sleep(4 * time.Millisecond)
		}
	}()
	<-ready
	if a.hwnd.Load() == 0 {
		t.Fatal("CreateWindowExW failed")
	}
	time.Sleep(300 * time.Millisecond)
	return a
}

func (a *animatedWindow) close() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
	<-a.stopped
}
