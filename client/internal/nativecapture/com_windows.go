//go:build windows

package nativecapture

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Minimal pure-Go COM/WinRT plumbing: interface pointers are uintptr values
// whose first word is the vtable; method indices come from the Windows SDK
// headers (d3d11.h, mftransform.h, mfobjects.h, windows.graphics.capture.h).

type guid = windows.GUID

func mustGUID(s string) guid {
	g, err := windows.GUIDFromString("{" + s + "}")
	if err != nil {
		panic(err)
	}
	return g
}

type hresult uintptr

func (h hresult) failed() bool { return int32(h) < 0 }

func (h hresult) err(op string) error {
	if !h.failed() {
		return nil
	}
	return fmt.Errorf("%s: HRESULT 0x%08X", op, uint32(h))
}

// vcall invokes vtable method idx of COM interface obj. uintptrescapes keeps
// pointer arguments (converted at the call site) alive and unmoved.
//
//go:uintptrescapes
func vcall(obj uintptr, idx int, args ...uintptr) hresult {
	if obj == 0 {
		return hresult(0x80004003) // E_POINTER
	}
	vtbl := *(*uintptr)(unsafe.Pointer(obj))                                         // #nosec G103 -- COM ABI
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0)))) // #nosec G103 -- COM ABI
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return hresult(r)
}

func release(obj uintptr) {
	if obj != 0 {
		vcall(obj, 2)
	}
}

func queryInterface(obj uintptr, iid *guid) (uintptr, error) {
	var out uintptr
	if hr := vcall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); hr.failed() { // #nosec G103
		return 0, hr.err("QueryInterface")
	}
	return out, nil
}

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
)

// initThread joins the multithreaded apartment on the current (locked) OS thread.
func initThread() {
	procRoInitialize.Call(1) // RO_INIT_MULTITHREADED; S_FALSE/RPC_E_CHANGED_MODE are fine.
}

func activationFactory(class string, iid *guid) (uintptr, error) {
	name, err := windows.UTF16FromString(class)
	if err != nil {
		return 0, err
	}
	var hs uintptr
	// #nosec G103 -- WindowsCreateString copies the UTF-16 buffer.
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)-1), uintptr(unsafe.Pointer(&hs))); hresult(r).failed() {
		return 0, hresult(r).err("WindowsCreateString")
	}
	defer procWindowsDeleteString.Call(hs)
	var factory uintptr
	// #nosec G103 -- out pointer for the activation factory.
	if r, _, _ := procRoGetActivationFactory.Call(hs, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory))); hresult(r).failed() {
		return 0, hresult(r).err("RoGetActivationFactory " + class)
	}
	return factory, nil
}

// closeWinRT calls IClosable::Close and releases the object.
func closeWinRT(obj uintptr) {
	if obj == 0 {
		return
	}
	if closable, err := queryInterface(obj, &iidIClosable); err == nil {
		vcall(closable, 6)
		release(closable)
	}
	release(obj)
}

var iidIClosable = mustGUID("30D5A829-7FA4-4026-83BB-D75BAE4EA99E")
