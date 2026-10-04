//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
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
	overlayBegin           = user32.NewProc("BeginPaint")
	overlayEnd             = user32.NewProc("EndPaint")
	overlayDraw            = user32.NewProc("DrawTextW")
	overlayWorkArea        = user32.NewProc("SystemParametersInfoW")
	overlayColor           = overlayGDI.NewProc("SetTextColor")
	overlayBackground      = overlayGDI.NewProc("SetBkMode")
	overlayDelete          = overlayGDI.NewProc("DeleteObject")
	overlaySelect          = overlayGDI.NewProc("SelectObject")
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
		// #nosec G103 -- SystemParametersInfo fills the ABI-shaped rectangle synchronously.
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
		var paint overlayPaint
		// #nosec G103 -- BeginPaint fills this ABI-shaped value; EndPaint consumes it on the same OS thread.
		_, _, _ = overlayBegin.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		// #nosec G103 -- Paired synchronous cleanup of the BeginPaint value above.
		_, _, _ = overlayEnd.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		return 0
	}

	result, _, _ := overlayDefault.Call(hwnd, message, wparam, lparam)
	return result
}

// createGamingOverlayLocked initializes the platform window while overlayMu is held.
func (a *App) createGamingOverlayLocked() string {
	window, err := newNativeGamingOverlay()
	if err != nil {
		return err.Error()
	}
	a.overlay = window
	return ""
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
	hwnd, _, err := overlayCreate.Call(0x080800A8, uintptr(unsafe.Pointer(overlayClassName)), uintptr(unsafe.Pointer(overlayClassName)), 0x80000000, 0, 0, 280, 64, 0, 0, 0, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("create gaming overlay: %w", err)
		return
	}
	defer func() { _, _, _ = overlayDestroy.Call(hwnd) }()
	overlayWindows.Store(hwnd, w)
	defer overlayWindows.Delete(hwnd)
	ready <- nil
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	lastError := ""
	reportError := func(err error) {
		if message := err.Error(); message != lastError {
			log.Printf("gaming overlay: %s", message)
			lastError = message
		}
	}
	var renderer *overlayRenderer
	var surface *overlaySurface
	defer func() {
		if surface != nil {
			surface.close()
		}
	}()
	started := time.Now()
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
			// #nosec G103 -- DispatchMessage synchronously reads the message filled by PeekMessage.
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
			last, renderer = "", nil
			if surface != nil {
				surface.close()
				surface = nil
			}
			continue
		}
		if time.Since(monitorRefresh) > time.Second {
			monitors, monitorRefresh = nativeGamingOverlayMonitors(), time.Now()
		}
		monitor := selectOverlayMonitor(monitors, s.Monitor)
		x, y, width, height := gamingOverlayPlacement(s, monitor)
		encoded, _ := json.Marshal(s)
		key := fmt.Sprintf("%s|%d,%d,%d,%d", encoded, x, y, width, height)
		if key == last && visible && !s.Animate {
			continue
		}
		if key != last || renderer == nil {
			if surface != nil {
				surface.close()
				surface = nil
			}
			renderer, err = newOverlayRenderer(s, int(width), int(height), nativeOverlayLabel)
			if err == nil {
				surface, err = newOverlaySurface(int(width), int(height))
			}
			if err != nil {
				reportError(err)
				_, _, _ = overlayShow.Call(hwnd, 0)
				visible = false
				continue
			}
			last = key
		}
		seconds := time.Since(started).Seconds()
		if !s.Animate {
			seconds = 0
		}
		if err = surface.present(hwnd, renderer.render(seconds), x, y, s.Opacity); err != nil {
			reportError(err)
			_, _, _ = overlayShow.Call(hwnd, 0)
			visible = false
			continue
		}
		if !visible {
			// UpdateLayeredWindow already set geometry; show topmost without activation.
			_, _, _ = overlayPosition.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, 0x53)
		}
		visible = true
		lastError = ""
	}
}
