package main

import (
	"testing"
	"unsafe"
)

func TestDisplayCaptureWindowsReasonContextLayout(t *testing.T) {
	context := displayCaptureReasonContext{}
	pointer := unsafe.Sizeof(uintptr(0))
	wantSize := uintptr(24)
	if pointer == 8 {
		wantSize = 32
	}
	if unsafe.Offsetof(context.version) != 0 || unsafe.Offsetof(context.flags) != 4 || unsafe.Offsetof(context.simpleReason) != 8 || unsafe.Sizeof(context) != wantSize {
		t.Fatalf("REASON_CONTEXT size=%d pointer=%d offsets=%d/%d/%d", unsafe.Sizeof(context), pointer, unsafe.Offsetof(context.version), unsafe.Offsetof(context.flags), unsafe.Offsetof(context.simpleReason))
	}
}

func TestDisplayCaptureWindowsPowerEventsAreAtomicAndPassive(t *testing.T) {
	backend := &windowsDisplayCapturePower{} // No loaded procedures: callbacks must not invoke OS APIs.
	backend.powerEvent(4)
	if backend.transition.Load() != 3 {
		t.Fatal("suspend did not block first generation")
	}
	backend.powerEvent(18)
	if backend.transition.Load() != 5 {
		t.Fatal("automatic resume became interactive")
	}
	backend.powerEvent(7)
	if backend.transition.Load() != 6 {
		t.Fatal("interactive resume did not permit next generation")
	}
	backend.powerEvent(999)
	if backend.transition.Load() != 6 {
		t.Fatal("unrelated event affected power state")
	}
	if err := backend.shutdown(); err != nil {
		t.Fatal(err)
	}
	backend.powerEvent(4) // A queued callback after shutdown only changes inert atomic state.
	if _, _, err := backend.state(); err == nil {
		t.Fatal("shutdown callback resurrected backend")
	}
}
