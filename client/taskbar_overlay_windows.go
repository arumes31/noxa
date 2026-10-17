package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var taskbarOverlayOwner struct {
	sync.Mutex
	backend *taskbarOverlayWindows
	stopped bool
}

// Same-process, thread-specific WH_CALLWNDPROC requires hmod=NULL. This hook
// never intercepts input or another process, and only observes two messages.
// https://learn.microsoft.com/windows/win32/api/winuser/nf-winuser-setwindowshookexw
// SetOverlayIcon copies the cached small-icon-size glyph disc (no logo; the
// button already shows it). Explorer restarts require reapply.
// https://learn.microsoft.com/windows/win32/api/shobjidl_core/nf-shobjidl_core-itaskbarlist3-setoverlayicon
type taskbarOverlayWindows struct {
	queue          *taskbarOverlayQueue
	hookMu         sync.Mutex
	hook           uintptr
	hookHWND       uintptr
	hookThread     uint32
	hookStopped    bool
	callback       uintptr // Go callback thunk and owner intentionally remain alive.
	createdMessage uint32
	target         atomic.Uintptr
	object         *taskbarCOMObject // COM owner thread only, including release.
	initialized    bool
	last           taskbarOverlayState
}

type taskbarCallWindow struct {
	lparam, wparam uintptr
	message        uint32
	hwnd           uintptr
}

func taskbarOwnedThread(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	// #nosec G103 -- Windows synchronously fills the owned DWORD process ID.
	thread, _, _ := procGetWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != uint32(os.Getpid()) {
		return 0
	}
	return uint32(thread)
}
func newTaskbarOverlayWindows(q *taskbarOverlayQueue) (*taskbarOverlayWindows, error) {
	if err := taskbarCallNextHook.Find(); err != nil {
		return nil, err
	}
	name, _ := windows.UTF16PtrFromString("TaskbarButtonCreated")
	// #nosec G103 -- The UTF-16 name remains alive through the synchronous call.
	msg, _, err := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if msg == 0 {
		return nil, fmt.Errorf("register taskbar message: %w", err)
	}
	b := &taskbarOverlayWindows{queue: q, createdMessage: uint32(msg)}
	b.callback = syscall.NewCallback(func(code int32, wparam uintptr, lparam unsafe.Pointer) uintptr {
		if code >= 0 && lparam != nil && !q.stopped.Load() {
			// CWPSTRUCT is borrowed only for the synchronous hook callback.
			p := (*taskbarCallWindow)(lparam)
			if p.hwnd == b.target.Load() && (p.message == b.createdMessage || p.message == 0x82) {
				q.windowEvent(p.hwnd, p.message == 0x82)
			}
		}
		result, _, _ := taskbarCallNextHook.Call(0, uintptr(code), wparam, uintptr(lparam))
		return result
	})
	return b, nil
}
func (b *taskbarOverlayWindows) watch(hwnd uintptr) error {
	b.hookMu.Lock()
	defer b.hookMu.Unlock()
	if b.hookStopped {
		return fmt.Errorf("taskbar observer stopped")
	}
	thread := taskbarOwnedThread(hwnd)
	if b.hookHWND == hwnd && b.hook != 0 && b.hookThread == thread && thread != 0 {
		return nil
	}
	b.target.Store(0)
	if b.hook != 0 {
		ok, _, err := user32.NewProc("UnhookWindowsHookEx").Call(b.hook)
		if ok == 0 {
			return fmt.Errorf("remove taskbar observer: %w", err)
		}
		b.hook = 0
		b.hookHWND = 0
	}
	if hwnd == 0 {
		return nil
	}
	if thread == 0 {
		return fmt.Errorf("taskbar window no longer owned")
	}
	hook, _, err := user32.NewProc("SetWindowsHookExW").Call(4, b.callback, 0, uintptr(thread))
	if hook == 0 {
		return fmt.Errorf("observe taskbar window: %w", err)
	}
	b.hook = hook
	b.hookHWND = hwnd
	b.hookThread = thread
	b.target.Store(hwnd)
	return nil
}
func (b *taskbarOverlayWindows) stopWatch() error {
	b.hookMu.Lock()
	defer b.hookMu.Unlock()
	b.hookStopped = true
	b.target.Store(0)
	if b.hook == 0 {
		return nil
	}
	// Callback takes no hookMu; native unhook cannot deadlock on this mutex.
	ok, _, err := user32.NewProc("UnhookWindowsHookEx").Call(b.hook)
	if ok == 0 {
		return fmt.Errorf("remove taskbar observer: %w", err)
	}
	b.hook = 0
	b.hookHWND = 0
	return nil
}

var taskbarCallNextHook = user32.NewProc("CallNextHookEx")

type taskbarCOMObject struct{ vtable *[21]uintptr }

var taskbarOle32 = windows.NewLazySystemDLL("ole32.dll")
var taskbarClass = windows.GUID{Data1: 0x56fdf344, Data2: 0xfd6d, Data3: 0x11d0, Data4: [8]byte{0x95, 0x8a, 0, 0x60, 0x97, 0xc9, 0xa0, 0x90}}
var taskbarInterface = windows.GUID{Data1: 0xea1afb91, Data2: 0x9e28, Data3: 0x4b86, Data4: [8]byte{0x90, 0xe9, 0x9e, 0x9f, 0x8a, 0x5e, 0xef, 0xaf}}

// Accessible overlay descriptions, indexed by trayIconState.
var taskbarDescriptionText = [trayStateCount]string{
	trayIdle:            "In a channel, microphone on",
	trayTalking:         "Talking",
	trayMicMuted:        "Microphone muted",
	trayDeafened:        "Audio muted",
	trayBothMuted:       "Microphone and audio muted",
	trayTalkingDeafened: "Talking; audio muted",
	trayDisconnected:    "Not connected",
	trayNoChannel:       "Connected, not in a channel",
}

var taskbarDescriptions = sync.OnceValue(func() [trayStateCount]*uint16 {
	var result [trayStateCount]*uint16
	for i, s := range taskbarDescriptionText {
		result[i], _ = windows.UTF16PtrFromString(s)
	}
	return result
})

func taskbarHRESULT(stage string, hr uintptr) error {
	if int32(uint32(hr)) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08x", stage, uint32(hr))
	}
	return nil
}
func taskbarCOMCall(object *taskbarCOMObject, index int, args ...uintptr) uintptr {
	// ITaskbarList3 vtable: IUnknown 0..2, HrInit 3, SetOverlayIcon 18.
	table := object.vtable
	// #nosec G103 -- The native COM interface is retained by this locked owner thread.
	values := append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)
	result, _, _ := syscall.SyscallN(table[index], values...)
	return result
}
func (b *taskbarOverlayWindows) release() {
	if b.object != nil {
		taskbarCOMCall(b.object, 2)
		b.object = nil
	}
}
func (b *taskbarOverlayWindows) connect() error {
	if !b.initialized {
		hr, _, _ := taskbarOle32.NewProc("CoInitializeEx").Call(0, 0) // Dedicated MTA thread.
		if err := taskbarHRESULT("taskbar COM initialize", hr); err != nil {
			return err
		}
		b.initialized = true
	}
	if b.object != nil {
		return nil
	}
	// #nosec G103 -- COM reads static GUIDs and writes the owner-held interface pointer synchronously.
	hr, _, _ := taskbarOle32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&taskbarClass)), 0, 1, uintptr(unsafe.Pointer(&taskbarInterface)), uintptr(unsafe.Pointer(&b.object)))
	if err := taskbarHRESULT("create taskbar overlay", hr); err != nil {
		return err
	}
	if b.object == nil {
		return fmt.Errorf("empty taskbar COM interface")
	}
	if err := taskbarHRESULT("initialize taskbar overlay", taskbarCOMCall(b.object, 3)); err != nil {
		b.release()
		return err
	}
	return nil
}
func (b *taskbarOverlayWindows) apply(s taskbarOverlayState) {
	if err := b.watch(s.hwnd); err != nil {
		if !b.queue.stopped.Load() {
			log.Printf("taskbar overlay: %v", err)
		}
		return
	}
	if s.hwnd == 0 {
		b.release()
		return
	}
	if b.last.hwnd != s.hwnd || b.last.generation != s.generation {
		b.release()
	}
	b.last = s
	if err := b.connect(); err != nil {
		log.Printf("taskbar overlay: %v", err)
		return
	}
	// Recheck after potentially slow COM initialization. A destroyed/replaced
	// window or shutdown must not receive an obsolete queued overlay.
	current := b.queue.desired.Load()
	if b.queue.stopped.Load() || current == nil || *current != s || taskbarOwnedThread(s.hwnd) == 0 {
		return
	}
	icon, err := taskbarOverlayIcon(s.mode)
	if err != nil {
		log.Printf("taskbar overlay: %v", err)
		return
	}
	descriptions := taskbarDescriptions()
	// #nosec G103 -- Cached UTF-16 descriptions and icon handles outlive the synchronous COM call.
	hr := taskbarCOMCall(b.object, 18, s.hwnd, icon, uintptr(unsafe.Pointer(descriptions[s.mode])))
	if err := taskbarHRESULT("set taskbar overlay", hr); err != nil {
		b.release()
		log.Printf("taskbar overlay: %v", err)
	}
}
func (b *taskbarOverlayWindows) closeCOM() {
	// Shell calls remain on this owner. Shutdown never waits indefinitely for it.
	if b.object != nil && taskbarOwnedThread(b.last.hwnd) != 0 {
		taskbarCOMCall(b.object, 18, b.last.hwnd, 0, 0)
	}
	b.release()
	if b.initialized {
		taskbarOle32.NewProc("CoUninitialize").Call()
		b.initialized = false
	}
}
func queueTaskbarOverlay(hwnd uintptr, mode trayIconState) error {
	if mode >= trayStateCount {
		return fmt.Errorf("invalid taskbar state")
	}
	taskbarOverlayOwner.Lock()
	defer taskbarOverlayOwner.Unlock()
	if taskbarOverlayOwner.stopped {
		return nil
	}
	if taskbarOverlayOwner.backend == nil {
		q := newTaskbarOverlayQueue()
		b, err := newTaskbarOverlayWindows(q)
		if err != nil {
			return err
		}
		taskbarOverlayOwner.backend = b
		go func() { runtime.LockOSThread(); defer runtime.UnlockOSThread(); q.run(b.apply, b.closeCOM) }()
	}
	taskbarOverlayOwner.backend.queue.publish(hwnd, mode)
	return nil
}
func shutdownTaskbarOverlay() error {
	taskbarOverlayOwner.Lock()
	taskbarOverlayOwner.stopped = true
	b := taskbarOverlayOwner.backend
	taskbarOverlayOwner.Unlock()
	if b == nil {
		return nil
	}
	b.queue.stopNow()
	err := b.stopWatch()
	if !b.queue.wait(250 * time.Millisecond) {
		return fmt.Errorf("taskbar overlay owner retained during shell shutdown")
	}
	return err
}
