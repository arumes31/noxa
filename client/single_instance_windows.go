//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/options"
	"golang.org/x/sys/windows"
)

// Wails' SetupSingleInstance protocol (v2 windows/single_instance.go): a named
// mutex plus a message-only window that receives the second launch's
// arguments as WM_COPYDATA. Replicating it lets a normally started noXa, which
// runs Wails' own check, forward to an update-started process (F8).
const (
	wmCopyData    = 0x004A
	hwndMessage   = ^uintptr(2) // HWND_MESSAGE (-3)
	wailsInstance = "wails-app-"
)

var (
	singleInstanceGetMessage = user32.NewProc("GetMessageW")
	singleInstanceProcedure  = syscall.NewCallback(singleInstanceWindowProc)
	// singleInstanceForward is set before the window thread starts.
	singleInstanceForward func(options.SecondInstanceData)
)

type singleInstanceCopyData struct {
	kind uintptr
	size uint32
	data uintptr
}

// tryAdoptSingleInstance takes the Wails lock for uniqueID if no process holds
// it. On success the mutex handle stays open for the process lifetime.
func tryAdoptSingleInstance(uniqueID string, forward func(options.SecondInstanceData)) bool {
	id := wailsInstance + uniqueID
	name, err := windows.UTF16PtrFromString(id + "sim")
	if err != nil {
		return false
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		// ERROR_ALREADY_EXISTS still returns a handle: an open one would keep
		// the name alive after the owner exits, with no window to forward to.
		if mutex != 0 {
			_ = windows.CloseHandle(mutex)
		}
		return false
	}
	singleInstanceForward = forward
	created := make(chan bool, 1)
	go runSingleInstanceWindow(id, created)
	if !<-created {
		_ = windows.CloseHandle(mutex) // a mutex without a window makes later launches run fully
		return false
	}
	return true
}

func runSingleInstanceWindow(id string, created chan<- bool) {
	runtime.LockOSThread() // a window's messages arrive only on its creating thread
	className, err1 := windows.UTF16PtrFromString(id + "-sic")
	windowName, err2 := windows.UTF16PtrFromString(id + "-siw")
	if err1 != nil || err2 != nil {
		created <- false
		return
	}
	class := overlayClass{Procedure: singleInstanceProcedure, Name: className}
	class.Size = uint32(unsafe.Sizeof(class))
	// A retry after a failed CreateWindowEx finds the class already registered.
	// #nosec G103 -- RegisterClassEx reads the class synchronously; the callback is package-level.
	_, _, _ = overlayRegister.Call(uintptr(unsafe.Pointer(&class)))
	// #nosec G103 -- UTF-16 names stay referenced until CreateWindowEx returns.
	hwnd, _, _ := overlayCreate.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)), 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	created <- hwnd != 0
	if hwnd == 0 {
		runtime.UnlockOSThread()
		return
	}
	var message overlayMessage
	for {
		// #nosec G103 -- GetMessage fills this ABI-shaped stack value synchronously.
		ok, _, _ := singleInstanceGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ok) <= 0 { // #nosec G115 -- GetMessage returns a BOOL (-1, 0 or positive).
			return
		}
		// #nosec G103 -- DispatchMessage synchronously reads the message filled by GetMessage.
		_, _, _ = overlayDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func singleInstanceWindowProc(hwnd, message, wparam, lparam uintptr) uintptr {
	if message != wmCopyData || lparam == 0 {
		result, _, _ := overlayDefault.Call(hwnd, message, wparam, lparam)
		return result
	}
	// #nosec G103 -- lparam is the system's COPYDATASTRUCT, valid for this message only.
	copied := (*singleInstanceCopyData)(*(*unsafe.Pointer)(unsafe.Pointer(&lparam)))
	units := int(copied.size / 2)
	if copied.data == 0 || units == 0 || units > singleInstanceCopyMax {
		return 0
	}
	// Read only the bytes sent (never scan past them for a NUL), copied before returning.
	// #nosec G103 -- the system mapped size bytes at data for the duration of this message.
	payload := append([]uint16(nil), unsafe.Slice((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&copied.data))), units)...)
	if data, ok := parseSecondInstanceCopy(copied.kind, payload); ok {
		// The sender waits in SendMessage: never block it on the Wails UI thread.
		go guardCrash("second instance", func() { singleInstanceForward(data) })
	}
	return 0
}
