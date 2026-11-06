//go:build windows

// Pure-Go implementations of IClassFactory and the undocumented
// INotificationActivationCallback (<NotificationActivationCallback.h>),
// adapted from go-toast v2 (Unlicense OR MIT). The callbacks are
// package-level because syscall.NewCallback never frees its trampolines.

package wintoast

import (
	"runtime"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

var (
	iidClassFactory                   = ole.NewGUID("{00000001-0000-0000-C000-000000000046}")
	iidNotificationActivationCallback = ole.NewGUID("{53E31837-6600-4A81-9395-75CFFE746F94}")
)

type classFactoryObject struct{ vtable *classFactoryVtbl }

type classFactoryVtbl struct {
	ole.IUnknownVtbl
	CreateInstance uintptr
	LockServer     uintptr
}

type activationCallbackObject struct{ vtable *activationCallbackVtbl }

type activationCallbackVtbl struct {
	ole.IUnknownVtbl
	Activate uintptr
}

var pinner runtime.Pinner

func init() {
	pinner.Pin(classFactory)
	pinner.Pin(classFactory.vtable)
	pinner.Pin(activationCallback)
	pinner.Pin(activationCallback.vtable)
}

var (
	staticRef = syscall.NewCallback(func(unsafe.Pointer) uintptr { return 1 })

	classFactory = &classFactoryObject{vtable: &classFactoryVtbl{
		IUnknownVtbl: ole.IUnknownVtbl{
			QueryInterface: syscall.NewCallback(func(this *classFactoryObject, riid *ole.GUID, out unsafe.Pointer) uintptr {
				if !ole.IsEqualGUID(riid, iidClassFactory) && !ole.IsEqualGUID(riid, ole.IID_IUnknown) {
					return ole.E_NOINTERFACE
				}
				*(**classFactoryObject)(out) = this
				return ole.S_OK
			}),
			AddRef:  staticRef,
			Release: staticRef,
		},
		CreateInstance: syscall.NewCallback(func(_ *classFactoryObject, outer *ole.IUnknown, riid *ole.GUID, out unsafe.Pointer) uintptr {
			if outer != nil {
				return 0x80040110 // CLASS_E_NOAGGREGATION
			}
			if !ole.IsEqualGUID(riid, iidNotificationActivationCallback) && !ole.IsEqualGUID(riid, ole.IID_IUnknown) {
				return ole.E_NOINTERFACE
			}
			*(**activationCallbackObject)(out) = activationCallback
			return ole.S_OK
		}),
		LockServer: syscall.NewCallback(func(*classFactoryObject, uintptr) uintptr { return ole.S_OK }),
	}}

	activationCallback = &activationCallbackObject{vtable: &activationCallbackVtbl{
		IUnknownVtbl: ole.IUnknownVtbl{
			QueryInterface: syscall.NewCallback(func(this *activationCallbackObject, riid *ole.GUID, out unsafe.Pointer) uintptr {
				if !ole.IsEqualGUID(riid, iidNotificationActivationCallback) && !ole.IsEqualGUID(riid, ole.IID_IUnknown) {
					return ole.E_NOINTERFACE
				}
				*(**activationCallbackObject)(out) = this
				return ole.S_OK
			}),
			AddRef:  staticRef,
			Release: staticRef,
		},
		// Activate(appUserModelId, invokedArgs, NOTIFICATION_USER_INPUT_DATA*, count).
		// The strings are copied before returning; the handler runs on its
		// own goroutine so COM is never blocked by application code.
		Activate: syscall.NewCallback(func(_ unsafe.Pointer, appID, arguments, data unsafe.Pointer, count uint32) uintptr {
			id := windows.UTF16PtrToString((*uint16)(appID))
			args := windows.UTF16PtrToString((*uint16)(arguments))
			inputs := copyUserData(data, int(min(count, 16)))
			cb := currentCallback()
			go cb(id, args, inputs)
			return ole.S_OK
		}),
	}}
)

func copyUserData(ptr unsafe.Pointer, count int) []UserData {
	if ptr == nil || count <= 0 {
		return nil
	}
	type pair struct{ Key, Value *uint16 }
	raw := unsafe.Slice((*pair)(ptr), count)
	out := make([]UserData, 0, count)
	for _, item := range raw {
		out = append(out, UserData{Key: windows.UTF16PtrToString(item.Key), Value: windows.UTF16PtrToString(item.Value)})
	}
	return out
}
