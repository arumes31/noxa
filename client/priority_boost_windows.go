//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// D3DKMT_SCHEDULINGPRIORITYCLASS_HIGH needs no administrator rights (REALTIME does).
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/d3dkmthk/nf-d3dkmthk-d3dkmtsetprocessschedulingpriorityclass
const gpuPriorityHigh = 4

const (
	// gpuPriorityUntouched: the process's GPU priority could not be read; it is left alone.
	gpuPriorityUntouched   = -1
	statusInvalidParameter = 0xC000000D
)

var (
	gdi32                    = windows.NewLazySystemDLL("gdi32.dll")
	procSetGPUPriorityClass  = gdi32.NewProc("D3DKMTSetProcessSchedulingPriorityClass")
	procGetGPUPriorityClass  = gdi32.NewProc("D3DKMTGetProcessSchedulingPriorityClass")
	captureBoostRestoreMu    sync.Mutex
	captureBoostRestoreState = map[uint32]boostedProcess{}
)

type boostedProcess struct {
	cpu uint32
	gpu int32
}

// webViewProcesses returns this process and every msedgewebview2.exe below it
// (browser, GPU, utility/capture and the renderer running the E2EE worker).
func webViewProcesses() ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	children := map[uint32][]uint32{}
	names := map[uint32]string{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		names[entry.ProcessID] = strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
	}
	self := windows.GetCurrentProcessId()
	out := []uint32{self}
	for queue := []uint32{self}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if names[child] == "msedgewebview2.exe" && child != self {
				out = append(out, child)
				queue = append(queue, child)
			}
		}
	}
	return out, nil
}

func applyCaptureBoost(on bool) error {
	captureBoostRestoreMu.Lock()
	defer captureBoostRestoreMu.Unlock()
	if !on {
		var errs []error
		for pid, saved := range captureBoostRestoreState {
			errs = append(errs, setProcessPriorities(pid, saved.cpu, saved.gpu))
			delete(captureBoostRestoreState, pid)
		}
		return errors.Join(errs...)
	}
	pids, err := webViewProcesses()
	if err != nil {
		return err
	}
	var errs []error
	for _, pid := range pids {
		if _, done := captureBoostRestoreState[pid]; done {
			continue
		}
		saved, err := boostProcess(pid)
		errs = append(errs, err)
		if saved.cpu != 0 { // boosted (perhaps without the GPU part)
			captureBoostRestoreState[pid] = saved
		}
	}
	return errors.Join(errs...)
}

func openForPriority(pid uint32) (windows.Handle, error) {
	return windows.OpenProcess(windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
}

func boostProcess(pid uint32) (boostedProcess, error) {
	handle, err := openForPriority(pid)
	if err != nil {
		return boostedProcess{}, fmt.Errorf("OpenProcess pid %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	cpu, err := windows.GetPriorityClass(handle)
	if err != nil {
		return boostedProcess{}, fmt.Errorf("GetPriorityClass pid %d: %w", pid, err)
	}
	var gpu int32
	var gpuErr error
	// #nosec G103 -- the call writes one D3DKMT_SCHEDULINGPRIORITYCLASS.
	if status, _, _ := procGetGPUPriorityClass.Call(uintptr(handle), uintptr(unsafe.Pointer(&gpu))); status != 0 {
		// Sandboxed WebView2 processes (renderer, storage utility) answer
		// STATUS_INVALID_PARAMETER: they have no GPU scheduling context. They
		// still get the CPU boost; only other statuses are worth a log line.
		if uint32(status) != statusInvalidParameter {
			gpuErr = fmt.Errorf("D3DKMTGetProcessSchedulingPriorityClass pid %d: NTSTATUS 0x%08X", pid, uint32(status))
		}
		gpu = gpuPriorityUntouched
	}
	// Never lower a process that is already above the boost.
	newCPU := cpu
	if cpu == windows.NORMAL_PRIORITY_CLASS || cpu == windows.BELOW_NORMAL_PRIORITY_CLASS || cpu == windows.IDLE_PRIORITY_CLASS {
		newCPU = windows.ABOVE_NORMAL_PRIORITY_CLASS
	}
	newGPU := gpu
	if gpu != gpuPriorityUntouched {
		newGPU = max(gpu, gpuPriorityHigh)
	}
	if err := setHandlePriorities(handle, newCPU, newGPU); err != nil {
		return boostedProcess{}, fmt.Errorf("pid %d: %w", pid, err)
	}
	// Returned with gpuErr too: the pid is recorded, so a refresh never retries it.
	return boostedProcess{cpu: cpu, gpu: gpu}, gpuErr
}

func setProcessPriorities(pid, cpu uint32, gpu int32) error {
	handle, err := openForPriority(pid)
	if err != nil {
		return nil // The process ended; nothing to restore.
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return setHandlePriorities(handle, cpu, gpu)
}

func setHandlePriorities(handle windows.Handle, cpu uint32, gpu int32) error {
	if err := windows.SetPriorityClass(handle, cpu); err != nil {
		return err
	}
	if gpu == gpuPriorityUntouched {
		return nil
	}
	if status, _, _ := procSetGPUPriorityClass.Call(uintptr(handle), uintptr(gpu)); status != 0 {
		return fmt.Errorf("D3DKMTSetProcessSchedulingPriorityClass: NTSTATUS 0x%08X", uint32(status))
	}
	return nil
}
