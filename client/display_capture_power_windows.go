package main

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsDisplayCapturePower struct {
	createRequest *windows.LazyProc
	setRequest    *windows.LazyProc
	clearRequest  *windows.LazyProc
	register      *windows.LazyProc
	unregister    *windows.LazyProc
	notification  uintptr
	callback      uintptr
	closed        bool
	transition    atomic.Uint64
}

func newDisplayCapturePowerBackend() displayCapturePowerBackend {
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	user := windows.NewLazySystemDLL("user32.dll")
	return &windowsDisplayCapturePower{
		createRequest: kernel.NewProc("PowerCreateRequest"),
		setRequest:    kernel.NewProc("PowerSetRequest"),
		clearRequest:  kernel.NewProc("PowerClearRequest"),
		register:      user.NewProc("RegisterSuspendResumeNotification"),
		unregister:    user.NewProc("UnregisterSuspendResumeNotification"),
	}
}

// REASON_CONTEXT with the simple-string union member. The remaining union
// storage accommodates the native detailed context on both 32-bit and 64-bit.
type displayCaptureReasonContext struct {
	version      uint32
	flags        uint32
	simpleReason *uint16
	unionPadding [unsafe.Sizeof(uintptr(0)) + 8]byte
}

func (p *windowsDisplayCapturePower) create() (uintptr, error) {
	reason, err := windows.UTF16PtrFromString("noXa: active screen sharing")
	if err != nil {
		return 0, err
	}
	context := displayCaptureReasonContext{flags: 1, simpleReason: reason}
	// #nosec G103 -- PowerCreateRequest synchronously reads this owned REASON_CONTEXT and UTF-16 reason.
	handle, _, callErr := p.createRequest.Call(uintptr(unsafe.Pointer(&context)))
	runtime.KeepAlive(context)
	runtime.KeepAlive(reason)
	if handle == uintptr(windows.InvalidHandle) || handle == 0 {
		return 0, fmt.Errorf("PowerCreateRequest: %w", callErr)
	}
	return handle, nil
}

func (p *windowsDisplayCapturePower) set(handle uintptr, kind uint32) error {
	result, _, err := p.setRequest.Call(handle, uintptr(kind))
	if result == 0 {
		return fmt.Errorf("PowerSetRequest(%d): %w", kind, err)
	}
	return nil
}

func (p *windowsDisplayCapturePower) clear(handle uintptr, kind uint32) error {
	result, _, err := p.clearRequest.Call(handle, uintptr(kind))
	if result == 0 {
		return fmt.Errorf("PowerClearRequest(%d): %w", kind, err)
	}
	return nil
}

func (p *windowsDisplayCapturePower) close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}

// Suspend/resume callback API and interactive-resume ordering:
// https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-registersuspendresumenotification
// https://learn.microsoft.com/en-us/windows/win32/power/pbt-apmresumeautomatic
// The low bit is blocked; upper bits form a transition generation. A single
// atomic snapshot prevents combining a new generation with an old state.
func (p *windowsDisplayCapturePower) powerEvent(kind uint32) {
	if kind != 4 && kind != 7 && kind != 18 {
		return
	} // Suspend, ResumeSuspend, ResumeAutomatic.
	for {
		old := p.transition.Load()
		next := (old &^ 1) + 2
		if kind != 7 {
			next |= 1
		}
		if p.transition.CompareAndSwap(old, next) {
			return
		}
	}
}

func (p *windowsDisplayCapturePower) state() (uint64, bool, error) {
	if p.closed {
		return 0, true, fmt.Errorf("display capture power backend is closed")
	}
	if p.notification == 0 {
		if err := p.register.Find(); err != nil {
			return 0, false, err
		}
		if err := p.unregister.Find(); err != nil {
			return 0, false, err
		}
		if p.callback == 0 {
			// Go callback trampolines are process-lifetime allocations. Create only one
			// for this App's backend; native notification registration is released below.
			p.callback = windows.NewCallback(func(_ uintptr, kind uint32, _ uintptr) uintptr {
				// Never take the manager lock or call a power API from a native callback.
				p.powerEvent(kind)
				return 0
			})
		}
		subscription := struct{ callback, context uintptr }{callback: p.callback}
		// #nosec G103 -- RegisterSuspendResumeNotification synchronously reads this callback descriptor; context is null.
		handle, _, err := p.register.Call(uintptr(unsafe.Pointer(&subscription)), 2) // DEVICE_NOTIFY_CALLBACK
		runtime.KeepAlive(subscription)
		if handle == 0 {
			return 0, false, fmt.Errorf("RegisterSuspendResumeNotification: %w", err)
		}
		p.notification = handle
	}
	state := p.transition.Load()
	return state >> 1, state&1 != 0, nil
}

func (p *windowsDisplayCapturePower) shutdown() error {
	if p.closed {
		return nil
	}
	p.closed = true
	if p.notification == 0 {
		return nil
	}
	handle := p.notification
	p.notification = 0
	result, _, err := p.unregister.Call(handle)
	if result == 0 {
		return fmt.Errorf("UnregisterSuspendResumeNotification: %w", err)
	}
	return nil
}
