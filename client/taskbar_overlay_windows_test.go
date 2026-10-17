package main

import (
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTaskbarOverlayHookOnlyObservesOwnedHiddenWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	create := func() uintptr {
		h, _, e := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 32, 32, 0, 0, 0, 0)
		if h == 0 {
			t.Fatalf("hidden window: %v", e)
		}
		return h
	}
	first, other := create(), create()
	defer user32.NewProc("DestroyWindow").Call(other)
	q := newTaskbarOverlayQueue()
	q.publish(first, trayMicMuted)
	b, err := newTaskbarOverlayWindows(q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		q.stopNow()
		if err := b.stopWatch(); err != nil {
			t.Error(err)
		}
	}()
	// Install from another thread, exactly as the COM worker does in production.
	installed := make(chan error, 1)
	go func() { installed <- b.watch(first) }()
	select {
	case err := <-installed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("hook install blocked")
	}
	send := user32.NewProc("SendMessageW")
	old := q.desired.Load()
	send.Call(other, uintptr(b.createdMessage), 0, 0)
	if q.desired.Load() != old {
		t.Fatal("other window changed overlay")
	}
	send.Call(first, uintptr(b.createdMessage), 0, 0)
	if q.desired.Load().generation != old.generation+1 {
		t.Fatal("TaskbarButtonCreated was not observed")
	}
	user32.NewProc("DestroyWindow").Call(first)
	if q.desired.Load().hwnd != 0 {
		t.Fatal("WM_NCDESTROY failed to invalidate target")
	}
	if err := b.stopWatch(); err != nil {
		t.Fatal(err)
	}
	if b.target.Load() != 0 || b.hook != 0 {
		t.Fatal("hook retained active target")
	}
	// No native taskbar COM method or visible window is used by this test.
}
func TestTaskbarOverlayABIAndDescriptions(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && (unsafe.Sizeof(taskbarCallWindow{}) != 32 || unsafe.Offsetof(taskbarCallWindow{}.hwnd) != 24) {
		t.Fatal("CWPSTRUCT ABI mismatch")
	}
	desc := taskbarDescriptions()
	seen := map[string]bool{}
	for i, p := range desc {
		s := windows.UTF16PtrToString(p)
		if s == "" || seen[s] {
			t.Fatalf("missing unique accessible status %d", i)
		}
		seen[s] = true
	}
	if taskbarHRESULT("test", 0) != nil || taskbarHRESULT("test", 1) != nil || taskbarHRESULT("test", 0x80004005) == nil {
		t.Fatal("HRESULT success/failure classification")
	}
}

func TestTaskbarOverlayCOMVtableBoundary(t *testing.T) {
	var table [21]uintptr
	object := taskbarCOMObject{vtable: &table}
	var initialized, overlay, released bool
	table[3] = syscall.NewCallback(func(this *taskbarCOMObject) uintptr { initialized = this == &object; return 0 })
	table[18] = syscall.NewCallback(func(this *taskbarCOMObject, hwnd, icon uintptr, description *uint16) uintptr {
		overlay = this == &object && hwnd == 7 && icon == 11 && windows.UTF16PtrToString(description) == "Audio muted"
		return 0
	})
	table[2] = syscall.NewCallback(func(this *taskbarCOMObject) uintptr { released = this == &object; return 0 })
	if taskbarCOMCall(&object, 3) != 0 {
		t.Fatal("HrInit result")
	}
	descriptions := taskbarDescriptions()
	taskbarCOMCall(&object, 18, 7, 11, uintptr(unsafe.Pointer(descriptions[trayDeafened])))
	taskbarCOMCall(&object, 2)
	if !initialized || !overlay || !released {
		t.Fatal("ITaskbarList3 ABI dispatch mismatch")
	}
	runtime.KeepAlive(object)
}
func TestTaskbarOverlayHookBoundedMessageCost(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 32, 32, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer user32.NewProc("DestroyWindow").Call(hwnd)
	send := user32.NewProc("SendMessageW")
	measure := func() time.Duration {
		start := time.Now()
		for i := 0; i < 10000; i++ {
			send.Call(hwnd, 0, 0, 0)
		}
		return time.Since(start)
	}
	baseline := measure()
	q := newTaskbarOverlayQueue()
	q.publish(hwnd, trayIdle)
	b, err := newTaskbarOverlayWindows(q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		q.stopNow()
		if err := b.stopWatch(); err != nil {
			t.Error(err)
		}
	}()
	if err := b.watch(hwnd); err != nil {
		t.Fatal(err)
	}
	before := q.desired.Load()
	hooked := measure()
	if q.desired.Load() != before {
		t.Fatal("ordinary messages caused overlay work")
	}
	t.Logf("10000 hidden-window WM_NULL calls: baseline=%s, hooked=%s; synthetic callback cost, not streaming performance", baseline, hooked)
}
