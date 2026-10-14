//go:build windows

package edge

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This private bridge is used only by the finite sender trace controller. Calls
// and unsubscribe must run on the WebView's owning COM apartment. Cancellation
// suppresses delivery; it does not cancel a native command or release references
// still owned by WebView2. In particular a pending Start must remain deliverable
// if its eventual success is needed to issue End.
const senderTraceMaxJSONUnits = 8 * 1024 * 1024
const senderTraceMaxParamsBytes = 64 * 1024

var (
	senderTraceIUnknown     = windows.GUID{Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	senderTraceCompletedIID = windows.GUID{Data1: 0x5c4889f0, Data2: 0x5ef6, Data3: 0x4c5a, Data4: [8]byte{0x95, 0x2c, 0xd8, 0xf1, 0xb9, 0x2d, 0x05, 0x74}}
	senderTraceEventIID     = windows.GUID{Data1: 0xe2fda4be, Data2: 0x5456, Data3: 0x406c, Data4: [8]byte{0xa2, 0x61, 0x3d, 0x45, 0x21, 0x38, 0x36, 0x2c}}
	// Native references are invisible to the Go collector. The registry roots
	// each callback until its last COM Release, including after cancellation.
	senderTraceRoots sync.Map
)

type senderTraceHandlerVtbl struct {
	_IUnknownVtbl
	Invoke ComProc
}

type senderTraceHandler struct {
	vtbl     *senderTraceHandlerVtbl
	refs     atomic.Int32
	mu       sync.Mutex
	callback func(string, error)
	iid      windows.GUID
}

func senderTraceHRESULT(hr uintptr) error {
	if int32(uint32(hr)) < 0 {
		return fmt.Errorf("sender trace HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// LPCWSTR memory is borrowed for the duration of Invoke. Never free it. Event
// GetParameterObjectAsJson instead returns owned CoTaskMem, freed by its caller.
func senderTraceJSON(value *uint16) (string, error) {
	if value == nil {
		return "", errors.New("sender trace missing JSON")
	}
	units := make([]uint16, 0, 1024)
	for i := 0; i <= senderTraceMaxJSONUnits; i++ {
		unit := *(*uint16)(unsafe.Add(unsafe.Pointer(value), uintptr(i)*2))
		if unit == 0 {
			return string(utf16.Decode(units)), nil
		}
		if i == senderTraceMaxJSONUnits {
			break
		}
		units = append(units, unit)
	}
	return "", errors.New("sender trace JSON limit")
}

func newSenderTraceHandler(vtbl *senderTraceHandlerVtbl, iid windows.GUID, callback func(string, error)) *senderTraceHandler {
	h := &senderTraceHandler{vtbl: vtbl, callback: callback, iid: iid}
	h.refs.Store(1)
	senderTraceRoots.Store(h, struct{}{})
	return h
}

func (h *senderTraceHandler) cancel() {
	h.mu.Lock()
	h.callback = nil
	h.mu.Unlock()
}

func (h *senderTraceHandler) deliver(value string, err error, once bool) {
	h.mu.Lock()
	callback := h.callback
	if once {
		h.callback = nil
	}
	h.mu.Unlock()
	// No bridge lock is held while the controller may call End/unsubscribe.
	if callback != nil {
		callback(value, err)
	}
}

func senderTraceQueryInterface(h *senderTraceHandler, iid *windows.GUID, out *unsafe.Pointer) uintptr {
	if out == nil {
		return 0x80004003
	} // E_POINTER
	*out = nil
	if iid == nil {
		return 0x80004003
	}
	if *iid != senderTraceIUnknown && *iid != h.iid {
		return 0x80004002
	} // E_NOINTERFACE
	senderTraceAddRef(h)
	*out = unsafe.Pointer(h)
	return 0
}

func senderTraceAddRef(h *senderTraceHandler) uintptr { return uintptr(h.refs.Add(1)) }
func senderTraceRelease(h *senderTraceHandler) uintptr {
	refs := h.refs.Add(-1)
	if refs == 0 {
		h.cancel()
		senderTraceRoots.Delete(h)
	}
	return uintptr(refs)
}

func senderTraceCompleted(h *senderTraceHandler, hr uintptr, value *uint16) uintptr {
	err := senderTraceHRESULT(hr)
	var result string
	if err == nil {
		result, err = senderTraceJSON(value)
	}
	h.deliver(result, err, true)
	return 0
}

type senderTraceEventArgsVtbl struct {
	_IUnknownVtbl
	GetParameterObjectAsJSON ComProc
}
type senderTraceEventArgs struct{ vtbl *senderTraceEventArgsVtbl }

func senderTraceEvent(h *senderTraceHandler, _ *ICoreWebView2, args *senderTraceEventArgs) uintptr {
	h.mu.Lock()
	active := h.callback != nil
	h.mu.Unlock()
	if !active {
		return 0
	}
	if args == nil || args.vtbl == nil || args.vtbl.GetParameterObjectAsJSON == 0 {
		h.deliver("", errors.New("sender trace missing event args"), false)
		return 0
	}
	var value *uint16
	hr, _, _ := args.vtbl.GetParameterObjectAsJSON.Call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&value)))
	// This is an LPWSTR* output, not the current (nil) LPWSTR value.
	if value != nil {
		defer windows.CoTaskMemFree(unsafe.Pointer(value))
	}
	err := senderTraceHRESULT(hr)
	var result string
	if err == nil {
		result, err = senderTraceJSON(value)
	}
	h.deliver(result, err, false)
	return 0
}

// Allocate native trampoline slots once, never per method call/subscription.
var senderTraceCompletedVtbl = senderTraceHandlerVtbl{
	_IUnknownVtbl: _IUnknownVtbl{NewComProc(senderTraceQueryInterface), NewComProc(senderTraceAddRef), NewComProc(senderTraceRelease)},
	Invoke:        NewComProc(senderTraceCompleted),
}
var senderTraceEventVtbl = senderTraceHandlerVtbl{
	_IUnknownVtbl: _IUnknownVtbl{NewComProc(senderTraceQueryInterface), NewComProc(senderTraceAddRef), NewComProc(senderTraceRelease)},
	Invoke:        NewComProc(senderTraceEvent),
}

func (core *ICoreWebView2) callSenderTraceMethod(method, params string, callback func(string, error)) (func(), error) {
	switch method {
	case "Tracing.start", "Tracing.end", "Performance.enable", "Performance.getMetrics", "Performance.disable":
	default:
		return nil, errors.New("sender trace method not allowed")
	}
	if callback == nil || core == nil || core.vtbl == nil || core.vtbl.CallDevToolsProtocolMethod == 0 {
		return nil, errors.New("sender trace method unavailable")
	}
	if len(params) > senderTraceMaxParamsBytes {
		return nil, errors.New("sender trace parameters limit")
	}
	name, err := windows.UTF16PtrFromString(method)
	if err != nil {
		return nil, errors.New("sender trace invalid method")
	}
	parameters, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return nil, errors.New("sender trace invalid parameters")
	}
	h := newSenderTraceHandler(&senderTraceCompletedVtbl, senderTraceCompletedIID, callback)
	hr, _, _ := core.vtbl.CallDevToolsProtocolMethod.Call(uintptr(unsafe.Pointer(core)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(parameters)), uintptr(unsafe.Pointer(h)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(parameters)
	err = senderTraceHRESULT(hr)
	if err != nil {
		h.cancel()
	}
	// WebView2 AddRefs callbacks it retains after this call.
	senderTraceRelease(h)
	if err != nil {
		return nil, err
	}
	return h.cancel, nil
}

type senderTraceReceiverVtbl struct {
	_IUnknownVtbl
	AddEvent    ComProc
	RemoveEvent ComProc
}
type senderTraceReceiver struct{ vtbl *senderTraceReceiverVtbl }

func senderTraceRemoveEvent(receiver *senderTraceReceiver, token int64) error {
	var hr uintptr
	if unsafe.Sizeof(uintptr(0)) == 4 {
		// EventRegistrationToken is an eight-byte struct passed by value. x86
		// uses two stack words; x64/ARM64 uses one full-width argument.
		hr, _, _ = receiver.vtbl.RemoveEvent.Call(uintptr(unsafe.Pointer(receiver)), uintptr(uint32(token)), uintptr(uint32(uint64(token)>>32)))
	} else {
		hr, _, _ = receiver.vtbl.RemoveEvent.Call(uintptr(unsafe.Pointer(receiver)), uintptr(uint64(token)))
	}
	return senderTraceHRESULT(hr)
}

func (core *ICoreWebView2) subscribeSenderTraceEvent(name string, callback func(string, error)) (func() error, error) {
	switch name {
	case "Tracing.dataCollected", "Tracing.tracingComplete", "Tracing.bufferUsage":
	default:
		return nil, errors.New("sender trace event not allowed")
	}
	if callback == nil || core == nil || core.vtbl == nil || core.vtbl.GetDevToolsProtocolEventReceiver == 0 {
		return nil, errors.New("sender trace event unavailable")
	}
	eventName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, errors.New("sender trace invalid event")
	}
	var receiver *senderTraceReceiver
	hr, _, _ := core.vtbl.GetDevToolsProtocolEventReceiver.Call(uintptr(unsafe.Pointer(core)), uintptr(unsafe.Pointer(eventName)), uintptr(unsafe.Pointer(&receiver)))
	runtime.KeepAlive(eventName)
	if err = senderTraceHRESULT(hr); err != nil {
		return nil, err
	}
	if receiver == nil || receiver.vtbl == nil {
		return nil, errors.New("sender trace missing event receiver")
	}
	releaseReceiver := func() { _, _, _ = receiver.vtbl.Release.Call(uintptr(unsafe.Pointer(receiver))) }
	if receiver.vtbl.AddEvent == 0 || receiver.vtbl.RemoveEvent == 0 {
		releaseReceiver()
		return nil, errors.New("sender trace invalid event receiver")
	}
	h := newSenderTraceHandler(&senderTraceEventVtbl, senderTraceEventIID, callback)
	var token int64
	hr, _, _ = receiver.vtbl.AddEvent.Call(uintptr(unsafe.Pointer(receiver)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
	err = senderTraceHRESULT(hr)
	if err != nil {
		h.cancel()
	}
	senderTraceRelease(h)
	if err != nil {
		releaseReceiver()
		return nil, err
	}
	removed := false
	return func() error {
		// Owner-STA only. A failed removal retains the exact receiver/token for
		// an explicit retry; delivery is suppressed even on that failure.
		if removed {
			return nil
		}
		h.cancel()
		if err := senderTraceRemoveEvent(receiver, token); err != nil {
			return err
		}
		removed = true
		releaseReceiver()
		return nil
	}, nil
}
