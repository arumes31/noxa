//go:build windows

package webview2

import (
	"golang.org/x/sys/windows"
	"runtime"
	"syscall"
	"unsafe"
)

type ICoreWebView2EnvironmentOptions4Vtbl struct {
	IUnknownVtbl
	GetCustomSchemeRegistrations ComProc
	SetCustomSchemeRegistrations ComProc
}

type ICoreWebView2EnvironmentOptions4 struct {
	Vtbl *ICoreWebView2EnvironmentOptions4Vtbl
}

func (i *ICoreWebView2EnvironmentOptions4) AddRef() uintptr {
	refCounter, _, _ := i.Vtbl.AddRef.Call(uintptr(unsafe.Pointer(i)))
	return refCounter
}

func (i *ICoreWebView2EnvironmentOptions4) GetCustomSchemeRegistrations() ([]*ICoreWebView2CustomSchemeRegistration, error) {

	var count uint32
	var schemeRegistrations **ICoreWebView2CustomSchemeRegistration

	hr, _, _ := i.Vtbl.GetCustomSchemeRegistrations.Call(
		uintptr(unsafe.Pointer(i)),
		uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&schemeRegistrations)),
	)
	if windows.Handle(hr) != windows.S_OK {
		return nil, syscall.Errno(hr)
	}
	if schemeRegistrations == nil {
		return nil, nil
	}
	// The callee returns AddRef'd interfaces in a CoTaskMemAlloc array; the
	// caller owns the references and must Release them.
	registrations := append([]*ICoreWebView2CustomSchemeRegistration(nil), unsafe.Slice(schemeRegistrations, count)...)
	CoTaskMemFree(unsafe.Pointer(schemeRegistrations))
	return registrations, nil
}

// SetCustomSchemeRegistrations passes the registration count by value and the
// registration pointer array directly, as the native setter requires.
func (i *ICoreWebView2EnvironmentOptions4) SetCustomSchemeRegistrations(schemeRegistrations []*ICoreWebView2CustomSchemeRegistration) error {

	var array uintptr
	if len(schemeRegistrations) > 0 {
		array = uintptr(unsafe.Pointer(&schemeRegistrations[0]))
	}

	hr, _, _ := i.Vtbl.SetCustomSchemeRegistrations.Call(
		uintptr(unsafe.Pointer(i)),
		uintptr(uint32(len(schemeRegistrations))),
		array,
	)
	runtime.KeepAlive(schemeRegistrations)
	if windows.Handle(hr) != windows.S_OK {
		return syscall.Errno(hr)
	}
	return nil
}
