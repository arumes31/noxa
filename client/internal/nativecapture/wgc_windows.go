//go:build windows

package nativecapture

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iidIGraphicsCaptureItemInterop   = mustGUID("3628E81B-3CAC-4C60-B7F4-23CE0E0C3356")
	iidIGraphicsCaptureItem          = mustGUID("79c3f95b-31f7-4ec2-a464-632ef5d30760")
	iidIFramePoolStatics2            = mustGUID("589b103f-6bbc-5df5-a991-02e28b3b66d5")
	iidIGraphicsCaptureSession2      = mustGUID("2c39ae40-7d2e-5044-804e-8b6799d4cf9e")
	iidIGraphicsCaptureSessionStatic = mustGUID("2224a540-5974-49aa-b232-0882536f4cb5")
	iidIGraphicsCaptureSession3      = mustGUID("f2cdd966-22ae-5ea1-9596-3a289344c3be")
	user32                           = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplayMonitors          = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")
	procIsWindow                     = user32.NewProc("IsWindow")
	procIsIconic                     = user32.NewProc("IsIconic")
	procEnumWindows                  = user32.NewProc("EnumWindows")
	procIsWindowVisible              = user32.NewProc("IsWindowVisible")
	procGetWindowTextW               = user32.NewProc("GetWindowTextW")
	procGetWindowLongPtrW            = user32.NewProc("GetWindowLongPtrW")
	procGetForegroundWindow          = user32.NewProc("GetForegroundWindow")
	procGetWindowRect                = user32.NewProc("GetWindowRect")
	procMonitorFromWindow            = user32.NewProc("MonitorFromWindow")
	procWindowThreadProcessID        = user32.NewProc("GetWindowThreadProcessId")
	procDwmGetWindowAttribute        = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
	enumMu                           sync.Mutex
	enumMonitors                     []monitorInfo
	enumWindowsOut                   []Window
	monitorCallback, windowsCallback = windows.NewCallback(onMonitor), windows.NewCallback(onWindow)
)

type monitorInfo struct {
	handle  uintptr
	rect    rect
	primary bool
}

func onMonitor(handle, _, _, _ uintptr) uintptr {
	info := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{Size: 40}
	_, _, _ = procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info))) // #nosec G103
	enumMonitors = append(enumMonitors, monitorInfo{handle: handle, rect: info.Monitor, primary: info.Flags&1 != 0})
	return 1
}

func onWindow(hwnd, _ uintptr) uintptr {
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return 1
	}
	if style, _, _ := procGetWindowLongPtrW.Call(hwnd, ^uintptr(19)); style&0x80 != 0 { // GWL_EXSTYLE (-20), WS_EX_TOOLWINDOW
		return 1
	}
	var cloaked uint32                                                                                                     // suspended UWP apps and other virtual-desktop windows render nothing
	if r, _, _ := procDwmGetWindowAttribute.Call(hwnd, 14, uintptr(unsafe.Pointer(&cloaked)), 4); r == 0 && cloaked != 0 { // #nosec G103 -- DWMWA_CLOAKED
		return 1
	}
	var pid uint32
	_, _, _ = procWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid))) // #nosec G103
	if pid == uint32(os.Getpid()) {                                               // #nosec G115 -- noXa itself is never a share source
		return 1
	}
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) // #nosec G103
	if n > 0 && len(enumWindowsOut) < 256 {
		exe := processExe(pid)
		// The desktop (Program Manager) covers every monitor but is no game.
		enumWindowsOut = append(enumWindowsOut, Window{Handle: uint64(hwnd), Title: windows.UTF16ToString(buf[:n]),
			Exe: exe, Fullscreen: !strings.EqualFold(exe, "explorer.exe") && coversMonitor(hwnd)})
	}
	return 1
}

// processExe is the executable file name of pid, or "".
func processExe(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}

// coversMonitor reports whether hwnd covers its whole monitor.
func coversMonitor(hwnd uintptr) bool {
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 { // #nosec G103
		return false
	}
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	info := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{Size: 40}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 { // #nosec G103
		return false
	}
	m := info.Monitor
	return r.Left <= m.Left && r.Top <= m.Top && r.Right >= m.Right && r.Bottom >= m.Bottom
}

func monitors() []monitorInfo {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumMonitors = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, monitorCallback, 0)
	return append([]monitorInfo(nil), enumMonitors...)
}

var (
	supportOnce sync.Once
	supported   bool
)

// Supported reports whether this Windows can share natively: Windows
// Graphics Capture (Windows 10 1903 and later) and Media Foundation (absent
// on N editions without the Media Feature Pack). Probed once.
func Supported() bool {
	supportOnce.Do(func() {
		done := make(chan bool)
		go func() {
			runtime.LockOSThread() // never unlocked: the MTA thread exits with this goroutine
			initThread()
			done <- probeSupport()
		}()
		supported = <-done
	})
	return supported
}

func probeSupport() bool {
	if procMFStartup.Find() != nil || procMFTEnumEx.Find() != nil {
		return false
	}
	statics, err := activationFactory("Windows.Graphics.Capture.GraphicsCaptureSession", &iidIGraphicsCaptureSessionStatic)
	if err != nil {
		return false
	}
	defer release(statics)
	var ok uint8
	return vcall(statics, 6, uintptr(unsafe.Pointer(&ok))).succeeded() && ok != 0 // #nosec G103 -- IsSupported
}

// sourceMonitor is the monitor src is shown on (0 for a Studio or an unknown monitor).
func sourceMonitor(src Source) uintptr {
	switch {
	case src.Studio:
		return 0
	case src.Window != 0:
		monitor, _, _ := procMonitorFromWindow.Call(uintptr(src.Window), 2) // MONITOR_DEFAULTTONEAREST
		return monitor
	}
	if list := monitors(); src.Monitor >= 0 && src.Monitor < len(list) {
		return list[src.Monitor].handle
	}
	return 0
}

// Monitors lists the displays that can be shared.
func Monitors() []Monitor {
	var out []Monitor
	for i, m := range monitors() {
		out = append(out, Monitor{Index: i, Width: int(m.rect.Right - m.rect.Left), Height: int(m.rect.Bottom - m.rect.Top), Primary: m.primary})
	}
	return out
}

// Windows lists visible, titled top-level windows.
func Windows() []Window {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumWindowsOut = nil
	_, _, _ = procEnumWindows.Call(windowsCallback, 0)
	return append([]Window(nil), enumWindowsOut...)
}

// ForegroundWindow returns the window the user is currently focused on.
func ForegroundWindow() uint64 {
	h, _, _ := procGetForegroundWindow.Call()
	return uint64(h)
}

// WindowState reports whether a captured window still exists and is minimized.
func WindowState(hwnd uint64) (alive, minimized bool) {
	a, _, _ := procIsWindow.Call(uintptr(hwnd))
	m, _, _ := procIsIconic.Call(uintptr(hwnd))
	return a != 0, m != 0
}

// capturer owns one Windows Graphics Capture session with a free-threaded
// frame pool; frames are polled, so no WinRT delegate is implemented in Go.
type capturer struct {
	d                   *device
	item, pool, session uintptr
	poolW, poolH        int32
}

type sizeInt32 struct{ W, H int32 }

func (s sizeInt32) packed() uintptr { return uintptr(uint32(s.W)) | uintptr(uint32(s.H))<<32 }

func newCapturer(d *device, src Source) (*capturer, error) {
	interop, err := activationFactory("Windows.Graphics.Capture.GraphicsCaptureItem", &iidIGraphicsCaptureItemInterop)
	if err != nil {
		return nil, err
	}
	defer release(interop)
	c := &capturer{d: d}
	if src.Window != 0 {
		if alive, _ := WindowState(src.Window); !alive {
			return nil, ErrSourceClosed
		}
		if hr := vcall(interop, 3, uintptr(src.Window), uintptr(unsafe.Pointer(&iidIGraphicsCaptureItem)), uintptr(unsafe.Pointer(&c.item))); hr.failed() { // #nosec G103
			return nil, hr.err("CreateForWindow")
		}
	} else {
		list := monitors()
		if src.Monitor < 0 || src.Monitor >= len(list) {
			return nil, errors.New("monitor not found")
		}
		if hr := vcall(interop, 4, list[src.Monitor].handle, uintptr(unsafe.Pointer(&iidIGraphicsCaptureItem)), uintptr(unsafe.Pointer(&c.item))); hr.failed() { // #nosec G103
			return nil, hr.err("CreateForMonitor")
		}
	}
	var size sizeInt32
	vcall(c.item, 7, uintptr(unsafe.Pointer(&size))) // #nosec G103 -- get_Size
	statics, err := activationFactory("Windows.Graphics.Capture.Direct3D11CaptureFramePool", &iidIFramePoolStatics2)
	if err != nil {
		c.close()
		return nil, err
	}
	defer release(statics)
	if hr := vcall(statics, 6, d.winrt, dxgiFormatB8G8R8A8, 2, size.packed(), uintptr(unsafe.Pointer(&c.pool))); hr.failed() { // #nosec G103
		c.close()
		return nil, hr.err("CreateFreeThreaded")
	}
	c.poolW, c.poolH = size.W, size.H
	if hr := vcall(c.pool, 10, c.item, uintptr(unsafe.Pointer(&c.session))); hr.failed() { // #nosec G103
		c.close()
		return nil, hr.err("CreateCaptureSession")
	}
	if s2, err := queryInterface(c.session, &iidIGraphicsCaptureSession2); err == nil {
		vcall(s2, 7, 1) // put_IsCursorCaptureEnabled(true)
		release(s2)
	}
	if s3, err := queryInterface(c.session, &iidIGraphicsCaptureSession3); err == nil {
		vcall(s3, 7, 0) // put_IsBorderRequired(false); refused on older builds, then the border stays.
		release(s3)
	}
	if hr := vcall(c.session, 6); hr.failed() {
		c.close()
		return nil, hr.err("StartCapture")
	}
	return c, nil
}

// latest drains the frame pool and hands the newest frame's texture to use
// before closing it. It reports whether a new frame arrived.
func (c *capturer) latest(use func(tex uintptr, w, h uint32)) (bool, error) {
	var last uintptr
	for {
		var frame uintptr
		if hr := vcall(c.pool, 7, uintptr(unsafe.Pointer(&frame))); hr.failed() { // #nosec G103 -- TryGetNextFrame
			closeWinRT(last)
			return false, hr.err("TryGetNextFrame")
		}
		if frame == 0 {
			break
		}
		closeWinRT(last)
		last = frame
	}
	if last == 0 {
		return false, nil
	}
	defer closeWinRT(last)
	var content sizeInt32
	vcall(last, 8, uintptr(unsafe.Pointer(&content))) // #nosec G103 -- get_ContentSize
	var surface uintptr
	if hr := vcall(last, 6, uintptr(unsafe.Pointer(&surface))); hr.failed() { // #nosec G103 -- get_Surface
		return false, hr.err("get_Surface")
	}
	defer release(surface)
	access, err := queryInterface(surface, &iidIDxgiInterfaceAcces)
	if err != nil {
		return false, err
	}
	defer release(access)
	var tex uintptr
	if hr := vcall(access, 3, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&tex))); hr.failed() { // #nosec G103
		return false, hr.err("GetInterface(ID3D11Texture2D)")
	}
	defer release(tex)
	w, h := uint32(min(content.W, c.poolW)), uint32(min(content.H, c.poolH))
	if content.W != c.poolW || content.H != c.poolH {
		// Window resized: future frames use a pool of the new size.
		if content.W > 0 && content.H > 0 {
			vcall(c.pool, 6, c.d.winrt, dxgiFormatB8G8R8A8, 2, content.packed()) // Recreate
			c.poolW, c.poolH = content.W, content.H
		}
	}
	if w > 0 && h > 0 {
		use(tex, w, h)
	}
	return true, nil
}

func (c *capturer) close() {
	closeWinRT(c.session)
	closeWinRT(c.pool)
	closeWinRT(c.item)
	c.session, c.pool, c.item = 0, 0, 0
}
