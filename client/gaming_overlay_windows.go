//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	overlayGDI             = windows.NewLazySystemDLL("gdi32.dll")
	overlayRegister        = user32.NewProc("RegisterClassExW")
	overlayCreate          = user32.NewProc("CreateWindowExW")
	overlayDefault         = user32.NewProc("DefWindowProcW")
	overlayDestroy         = user32.NewProc("DestroyWindow")
	overlayPeek            = user32.NewProc("PeekMessageW")
	overlayDispatch        = user32.NewProc("DispatchMessageW")
	overlayShow            = user32.NewProc("ShowWindow")
	overlayPosition        = user32.NewProc("SetWindowPos")
	overlayInvalidate      = user32.NewProc("InvalidateRect")
	overlayBegin           = user32.NewProc("BeginPaint")
	overlayEnd             = user32.NewProc("EndPaint")
	overlayFill            = user32.NewProc("FillRect")
	overlayDraw            = user32.NewProc("DrawTextW")
	overlayWorkArea        = user32.NewProc("SystemParametersInfoW")
	overlayAlpha           = user32.NewProc("SetLayeredWindowAttributes")
	overlayColor           = overlayGDI.NewProc("SetTextColor")
	overlayBackground      = overlayGDI.NewProc("SetBkMode")
	overlayBrush           = overlayGDI.NewProc("CreateSolidBrush")
	overlayDelete          = overlayGDI.NewProc("DeleteObject")
	overlaySelect          = overlayGDI.NewProc("SelectObject")
	overlayStock           = overlayGDI.NewProc("GetStockObject")
	overlayFont            = overlayGDI.NewProc("CreateFontW")
	overlayEnumMonitors    = user32.NewProc("EnumDisplayMonitors")
	overlayMonitorInfo     = user32.NewProc("GetMonitorInfoW")
	overlayMonitorMu       sync.Mutex
	overlayMonitorList     []GamingOverlayMonitor
	overlayMonitorCallback = syscall.NewCallback(collectOverlayMonitor)
	overlayClassName, _    = windows.UTF16PtrFromString("NoXaGamingOverlay")
	overlayRegisterOnce    sync.Once
	overlayRegisterError   error
	overlayWindows         sync.Map // HWND -> *nativeOverlay; callback never holds an App lock.
)

type overlayRect struct{ Left, Top, Right, Bottom int32 }
type overlayMonitorInfoEx struct {
	Size          uint32
	Monitor, Work overlayRect
	Flags         uint32
	Device        [32]uint16
}

func collectOverlayMonitor(handle, _dc, _rect, _data uintptr) uintptr {
	info := overlayMonitorInfoEx{}
	info.Size = uint32(unsafe.Sizeof(info))
	// #nosec G103 -- GetMonitorInfo fills a synchronous Win32 ABI value.
	ok, _, _ := overlayMonitorInfo.Call(handle, uintptr(unsafe.Pointer(&info)))
	if ok != 0 {
		id := windows.UTF16ToString(info.Device[:])
		overlayMonitorList = append(overlayMonitorList, GamingOverlayMonitor{ID: id, Name: id,
			Width: info.Monitor.Right - info.Monitor.Left, Height: info.Monitor.Bottom - info.Monitor.Top, Primary: info.Flags&1 != 0,
			workLeft: info.Work.Left, workTop: info.Work.Top, workWidth: info.Work.Right - info.Work.Left, workHeight: info.Work.Bottom - info.Work.Top})
	}
	return 1
}

func nativeGamingOverlayMonitors() []GamingOverlayMonitor {
	// EnumDisplayMonitors invokes the retained callback synchronously. Serialize
	// callers without passing a Go pointer into LPARAM or leaking callbacks.
	overlayMonitorMu.Lock()
	defer overlayMonitorMu.Unlock()
	overlayMonitorList = nil
	_, _, _ = overlayEnumMonitors.Call(0, 0, overlayMonitorCallback, 0)
	if len(overlayMonitorList) == 0 {
		var area overlayRect
		_, _, _ = overlayWorkArea.Call(0x30, 0, uintptr(unsafe.Pointer(&area)), 0)
		if area.Right > area.Left && area.Bottom > area.Top {
			overlayMonitorList = []GamingOverlayMonitor{{ID: "primary", Name: "Primary", Primary: true, Width: area.Right - area.Left, Height: area.Bottom - area.Top, workLeft: area.Left, workTop: area.Top, workWidth: area.Right - area.Left, workHeight: area.Bottom - area.Top}}
		}
	}
	return append([]GamingOverlayMonitor(nil), overlayMonitorList...)
}

type overlayPaint struct {
	DC                 uintptr
	Erase              int32
	Rect               overlayRect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type overlayMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}
type overlayClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type nativeOverlay struct {
	mu           sync.Mutex
	latest       GamingOverlaySnapshot
	updated      time.Time
	preview      GamingOverlaySnapshot
	previewUntil time.Time
	stop         chan struct{}
	done         chan struct{}
	once         sync.Once
	brush        uintptr
}

func nativeGamingOverlayAvailable() bool      { return true }
func isGamingOverlayWindow(hwnd uintptr) bool { _, ok := overlayWindows.Load(hwnd); return ok }

func overlayProcedure(hwnd, message, wparam, lparam uintptr) uintptr {
	if message == 0x84 {
		return ^uintptr(0)
	} // HTTRANSPARENT: pass input through.
	if message == 0x21 {
		return 3
	} // MA_NOACTIVATE.
	if message == 0x14 {
		return 1
	} // Paint owns the entire background.
	if message == 0x0F {
		if value, ok := overlayWindows.Load(hwnd); ok {
			window := value.(*nativeOverlay)
			window.mu.Lock()
			snapshot := window.latest
			if time.Now().Before(window.previewUntil) {
				snapshot = window.preview
			}
			window.mu.Unlock()
			var paint overlayPaint
			// #nosec G103 -- Win32 consumes ABI-shaped stack structs synchronously.
			dc, _, _ := overlayBegin.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
			defer func() { _, _, _ = overlayEnd.Call(hwnd, uintptr(unsafe.Pointer(&paint))) }()
			_, _, _ = overlayFill.Call(dc, uintptr(unsafe.Pointer(&paint.Rect)), window.brush)
			_, _, _ = overlayBackground.Call(dc, 1)
			fontHeight := int32(-14 * snapshot.Scale / 100)
			face, _ := windows.UTF16PtrFromString("Segoe UI")
			font, _, _ := overlayFont.Call(uintptr(fontHeight), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
			if font == 0 {
				font, _, _ = overlayStock.Call(17)
			} else {
				defer func() { _, _, _ = overlayDelete.Call(font) }()
			}
			old, _, _ := overlaySelect.Call(dc, font)
			defer func() { _, _, _ = overlaySelect.Call(dc, old) }()
			for i, line := range strings.Split(gamingOverlayText(snapshot), "\n") {
				color := uintptr(0x00E8E8E8)
				if strings.HasPrefix(line, "●") {
					color = 0x008AF046
				}
				_, _, _ = overlayColor.Call(dc, color)
				text, _ := windows.UTF16FromString(line)
				scale := int32(snapshot.Scale)
				rect := overlayRect{12 * scale / 100, int32(10+i*23) * scale / 100, 336 * scale / 100, int32(33+i*23) * scale / 100}
				_, _, _ = overlayDraw.Call(dc, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1), uintptr(unsafe.Pointer(&rect)), 0x8820) // SINGLELINE | NOPREFIX | END_ELLIPSIS
			}
			return 0
		}
	}
	result, _, _ := overlayDefault.Call(hwnd, message, wparam, lparam)
	return result
}

func newNativeGamingOverlay() (gamingOverlayWindow, error) {
	w := &nativeOverlay{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go w.run(ready)
	if err := <-ready; err != nil {
		<-w.done
		return nil, err
	}
	return w, nil
}
func (w *nativeOverlay) Update(s GamingOverlaySnapshot) {
	w.mu.Lock()
	w.latest = s
	w.updated = time.Now()
	w.mu.Unlock()
}
func (w *nativeOverlay) Preview(s GamingOverlaySnapshot) {
	w.mu.Lock()
	w.preview, w.previewUntil = s, time.Now().Add(5*time.Second)
	w.mu.Unlock()
}
func (w *nativeOverlay) Close() { w.once.Do(func() { close(w.stop) }); <-w.done }

func (w *nativeOverlay) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)
	overlayRegisterOnce.Do(func() {
		class := overlayClass{Procedure: syscall.NewCallback(overlayProcedure), Name: overlayClassName}
		class.Size = uint32(unsafe.Sizeof(class))
		// #nosec G103 -- RegisterClassEx reads the class synchronously; callback is retained by Go.
		atom, _, err := overlayRegister.Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 {
			overlayRegisterError = fmt.Errorf("register gaming overlay: %w", err)
		}
	})
	if overlayRegisterError != nil {
		ready <- overlayRegisterError
		return
	}
	// Layered + transparent + toolwindow + topmost + noactivate. No owner means
	// minimizing the main client does not hide the game overlay with it.
	// #nosec G103 -- Static UTF-16 class name is retained for the native window.
	hwnd, _, err := overlayCreate.Call(0x080800A8, uintptr(unsafe.Pointer(overlayClassName)), uintptr(unsafe.Pointer(overlayClassName)), 0x80000000, 0, 0, 348, 120, 0, 0, 0, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("create gaming overlay: %w", err)
		return
	}
	defer func() { _, _, _ = overlayDestroy.Call(hwnd) }()
	w.brush, _, _ = overlayBrush.Call(0x00251C12)
	defer func() { _, _, _ = overlayDelete.Call(w.brush) }()
	overlayWindows.Store(hwnd, w)
	defer overlayWindows.Delete(hwnd)
	_, _, _ = overlayAlpha.Call(hwnd, 0, 224, 2)
	ready <- nil
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	visible := false
	var monitors []GamingOverlayMonitor
	var monitorRefresh time.Time
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		var message overlayMessage
		for {
			// #nosec G103 -- PeekMessage fills this ABI-shaped stack value synchronously.
			ok, _, _ := overlayPeek.Call(uintptr(unsafe.Pointer(&message)), hwnd, 0, 0, 1)
			if ok == 0 {
				break
			}
			_, _, _ = overlayDispatch.Call(uintptr(unsafe.Pointer(&message)))
		}
		w.mu.Lock()
		s, updated := w.latest, w.updated
		if time.Now().Before(w.previewUntil) {
			s, updated = w.preview, time.Now()
		}
		w.mu.Unlock()
		if !s.Active || time.Since(updated) > 5*time.Second {
			if visible {
				_, _, _ = overlayShow.Call(hwnd, 0)
				visible = false
			}
			last = ""
			continue
		}
		if time.Since(monitorRefresh) > time.Second {
			monitors, monitorRefresh = nativeGamingOverlayMonitors(), time.Now()
		}
		monitor := selectOverlayMonitor(monitors, s.Monitor)
		x, y, width, height := gamingOverlayPlacement(s, monitor)
		text := fmt.Sprintf("%s|%d,%d,%d,%d,%d", gamingOverlayText(s), x, y, width, height, s.Opacity)
		if text == last && visible {
			continue
		}
		last = text
		_, _, _ = overlayAlpha.Call(hwnd, 0, uintptr(s.Opacity*255/100), 2)
		_, _, _ = overlayPosition.Call(hwnd, ^uintptr(0), uintptr(x), uintptr(y), uintptr(width), uintptr(height), 0x50) // SHOWWINDOW | NOACTIVATE
		_, _, _ = overlayInvalidate.Call(hwnd, 0, 1)
		visible = true
	}
}
