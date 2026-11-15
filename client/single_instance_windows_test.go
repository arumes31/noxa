//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/options"
	"golang.org/x/sys/windows"
)

// The adopted lock must be found and fed exactly as Wails' SetupSingleInstance
// in a normally started noXa does it: FindWindowW, then WM_COPYDATA.
func TestAdoptedSingleInstanceReceivesWailsForward(t *testing.T) {
	id := fmt.Sprintf("noxa-test-%d", os.Getpid()) // never the real noXa lock
	got := make(chan options.SecondInstanceData, 1)
	if !tryAdoptSingleInstance(id, func(data options.SecondInstanceData) { got <- data }) {
		t.Fatal("free lock not adopted")
	}
	if tryAdoptSingleInstance(id, func(options.SecondInstanceData) {}) {
		t.Fatal("held lock adopted twice")
	}
	className, _ := windows.UTF16PtrFromString(wailsInstance + id + "-sic")
	windowName, _ := windows.UTF16PtrFromString(wailsInstance + id + "-siw")
	// #nosec G103 -- synchronous Win32 call with retained UTF-16 strings.
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)))
	if hwnd == 0 {
		t.Fatal("FindWindowW does not find the adopted window")
	}
	serialized, _ := json.Marshal(options.SecondInstanceData{Args: []string{"noxa://h/invite/1#x"}, WorkingDirectory: `C:\`})
	units, _ := windows.UTF16FromString(string(serialized))
	// Wails sends len(units)*2+1 bytes, units including the NUL.
	copied := singleInstanceCopyData{kind: singleInstanceCopyKind, size: uint32(len(units)*2 + 1), data: uintptr(unsafe.Pointer(&units[0]))} // #nosec G103 G115 -- test payload
	// #nosec G103 -- SendMessage reads the COPYDATASTRUCT synchronously.
	_, _, _ = user32.NewProc("SendMessageW").Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&copied)))
	runtime.KeepAlive(units)
	select {
	case data := <-got:
		if len(data.Args) != 1 || data.Args[0] != "noxa://h/invite/1#x" || data.WorkingDirectory != `C:\` {
			t.Fatalf("forwarded %+v", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forwarded launch not delivered")
	}
}
