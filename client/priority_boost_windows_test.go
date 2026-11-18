//go:build windows

package main

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentPriorities(t *testing.T) (uint32, int32) {
	t.Helper()
	self := windows.CurrentProcess()
	cpu, err := windows.GetPriorityClass(self)
	if err != nil {
		t.Fatal(err)
	}
	var gpu int32
	// #nosec G103 -- the call writes one D3DKMT_SCHEDULINGPRIORITYCLASS.
	if status, _, _ := procGetGPUPriorityClass.Call(uintptr(self), uintptr(unsafe.Pointer(&gpu))); status != 0 {
		t.Fatalf("D3DKMTGetProcessSchedulingPriorityClass: 0x%x", status)
	}
	return cpu, gpu
}

// The boost reaches this process and is restored.
func TestCaptureBoostRaisesAndRestoresThisProcess(t *testing.T) {
	cpu, gpu := currentPriorities(t)
	if err := applyCaptureBoost(true); err != nil {
		t.Fatal(err)
	}
	boostedCPU, boostedGPU := currentPriorities(t)
	t.Logf("cpu 0x%x -> 0x%x, gpu %d -> %d", cpu, boostedCPU, gpu, boostedGPU)
	if boostedGPU < gpuPriorityHigh || cpu == windows.NORMAL_PRIORITY_CLASS && boostedCPU != windows.ABOVE_NORMAL_PRIORITY_CLASS {
		t.Fatalf("not boosted: cpu 0x%x gpu %d", boostedCPU, boostedGPU)
	}
	if err := applyCaptureBoost(false); err != nil {
		t.Fatal(err)
	}
	if restoredCPU, restoredGPU := currentPriorities(t); restoredCPU != cpu || restoredGPU != gpu {
		t.Fatalf("not restored: cpu 0x%x gpu %d", restoredCPU, restoredGPU)
	}
}
