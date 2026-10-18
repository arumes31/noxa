package edge

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"unsafe"
)

// This mode is deliberately native-only: the updater must authenticate and
// prepare its supervised handoff before selecting it. No environment variable,
// browser argument, or JavaScript binding can select the compatibility policy.
var updateBrowserStartup updateBrowserStartupState

// UpdateCompatibilityPolicy selects an exact, audited historical startup policy.
// Callers must authenticate the parent executable before selecting a policy.
type UpdateCompatibilityPolicy string

const (
	LegacyUpdateDefault           UpdateCompatibilityPolicy = "default"
	LegacyUpdateDuplicateFeatures UpdateCompatibilityPolicy = "duplicate-features"
	LegacyUpdateMergedHardware    UpdateCompatibilityPolicy = "merged-hardware"
)

type updateBrowserStartupState struct {
	mu      sync.Mutex
	started bool
	legacy  UpdateCompatibilityPolicy
	pid     atomic.Uint32
}

// SetLegacyUpdateCompatibility selects the shared legacy Wails browser options
// for the first, genuinely initialized stage of an authenticated update. Call
// before Wails starts. The updater must automatically restart this stage with
// the normal hardware policy after the original browser process has exited.
// It does not change the user data folder or persist a compatibility setting.
func SetLegacyUpdateCompatibility(policy UpdateCompatibilityPolicy) error {
	return updateBrowserStartup.enableLegacy(policy)
}

func (s *updateBrowserStartupState) enableLegacy(policy UpdateCompatibilityPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if policy != LegacyUpdateDefault && policy != LegacyUpdateDuplicateFeatures && policy != LegacyUpdateMergedHardware {
		return fmt.Errorf("unknown WebView2 update compatibility policy")
	}
	if s.started || s.legacy != "" {
		return fmt.Errorf("WebView2 update compatibility must be selected once before browser startup")
	}
	s.legacy = policy
	return nil
}

func (s *updateBrowserStartupState) arguments(fragments []string) (string, error) {
	s.mu.Lock()
	s.started = true
	legacy := s.legacy
	s.pid.Store(0)
	s.mu.Unlock()
	if legacy == "" {
		return hardwareBrowserArguments(fragments)
	}
	// The authenticated releases through v0.5.87 leave Windows options unset.
	// Wails adds precisely this fragment. Reject future/default drift instead of broadening the exception
	// into an arbitrary browser-argument override.
	const legacyArguments = "--disable-features=msSmartScreenProtection"
	if len(fragments) != 1 || fragments[0] != legacyArguments {
		return "", fmt.Errorf("unexpected browser options for the legacy update bridge")
	}
	switch legacy {
	case LegacyUpdateDuplicateFeatures:
		return "--disable-features=ForceSoftwareForRtcLowResolutions " + legacyArguments, nil
	case LegacyUpdateMergedHardware:
		return legacyArguments + ",ForceSoftwareForRtcLowResolutions", nil
	default:
		return legacyArguments, nil
	}
}

// UpdateBrowserProcessID returns the real WebView2 browser PID for this process,
// or zero before controller creation/after a failed PID query. The updater must
// open and retain a process handle before acknowledging or beginning a handoff;
// this numeric value alone must never be used later to terminate a process.
// Changed environment options require the old browser process to exit, not just
// its host: https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/process-related-events
func UpdateBrowserProcessID() uint32 {
	return updateBrowserStartup.pid.Load()
}

func (s *updateBrowserStartupState) recordBrowserProcessID(pid uint32, hr uintptr) error {
	s.pid.Store(0)
	if int32(hr) < 0 {
		return fmt.Errorf("get_BrowserProcessId failed with HRESULT 0x%08x", uint32(hr))
	}
	if pid == 0 {
		return fmt.Errorf("get_BrowserProcessId returned zero with HRESULT 0x%08x", uint32(hr))
	}
	s.pid.Store(pid)
	return nil
}

func (e *Chromium) captureUpdateBrowserProcessID() {
	var pid uint32
	hr, _, _ := e.webview.vtbl.GetBrowserProcessID.Call(
		uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(&pid)),
	)
	if err := updateBrowserStartup.recordBrowserProcessID(pid, hr); err != nil {
		// Ordinary startup may continue; a supervised update must fail safely if
		// the getter remains zero. Query failure must not invent a browser PID.
		log.Printf("[WebView2] update browser process unavailable: %v", err)
	}
}
