package edge

import (
	"strings"
	"sync"
	"testing"
)

func TestUpdateBrowserNormalPolicyIgnoresLegacyEnvironment(t *testing.T) {
	t.Setenv("NOXA_UPDATE_READY_ADDRESS", "127.0.0.1:43210")
	t.Setenv("NOXA_UPDATE_READY_NONCE", strings.Repeat("a", 64))
	t.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu")
	var state updateBrowserStartupState
	got, err := state.arguments([]string{"--disable-features=msSmartScreenProtection"})
	if err != nil {
		t.Fatal(err)
	}
	want := "--disable-features=msSmartScreenProtection,ForceSoftwareForRtcLowResolutions,MediaFoundationUseSWBRCForH264Camera"
	if got != want {
		t.Fatalf("ordinary boot policy = %q, want %q", got, want)
	}
}

func TestUpdateBrowserLegacyPolicyIsExactAndProcessLocal(t *testing.T) {
	var bridge updateBrowserStartupState
	if err := bridge.enableLegacy(); err != nil {
		t.Fatal(err)
	}
	fragments := []string{"--disable-features=msSmartScreenProtection"}
	got, err := bridge.arguments(fragments)
	if err != nil || got != fragments[0] {
		t.Fatalf("legacy boot policy = %q, %v", got, err)
	}
	var nextProcess updateBrowserStartupState
	got, err = nextProcess.arguments(fragments)
	if err != nil || !strings.Contains(got, "ForceSoftwareForRtcLowResolutions") || !strings.Contains(got, "MediaFoundationUseSWBRCForH264Camera") {
		t.Fatalf("next boot lost hardware policy: %q, %v", got, err)
	}
}

func TestUpdateBrowserLegacyRejectsUnexpectedOptions(t *testing.T) {
	for _, fragments := range [][]string{
		nil,
		{"--disable-features=msSmartScreenProtection", "--disable-gpu"},
		{"--disable-features=msSmartScreenProtection --remote-debugging-port=9222"},
		{"--disable-features=Other"},
		{"--disable-features=msSmartScreenProtection\x00"},
	} {
		var state updateBrowserStartupState
		if err := state.enableLegacy(); err != nil {
			t.Fatal(err)
		}
		if got, err := state.arguments(fragments); err == nil || got != "" {
			t.Fatalf("unexpected options accepted: %q -> %q, %v", fragments, got, err)
		}
	}
}

func TestUpdateBrowserCompatibilityCannotChangeAfterStartup(t *testing.T) {
	var state updateBrowserStartupState
	if _, err := state.arguments(nil); err != nil {
		t.Fatal(err)
	}
	if err := state.enableLegacy(); err == nil {
		t.Fatal("changed policy after startup")
	}
	var configured updateBrowserStartupState
	if err := configured.enableLegacy(); err != nil {
		t.Fatal(err)
	}
	if err := configured.enableLegacy(); err == nil {
		t.Fatal("accepted repeated compatibility preparation")
	}
}

func TestUpdateBrowserConcurrentSelectionHasOneWinner(t *testing.T) {
	var state updateBrowserStartupState
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- state.enableLegacy()
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("compatibility selections succeeded = %d, want 1", winners)
	}
}

func TestUpdateBrowserProcessIDFailsClosedAndClearsStaleID(t *testing.T) {
	var state updateBrowserStartupState
	if state.pid.Load() != 0 {
		t.Fatal("browser PID present before controller startup")
	}
	for _, failure := range []struct {
		pid uint32
		hr  uintptr
	}{
		{12345, 0x8007139f},
		{0, 0},
	} {
		if err := state.recordBrowserProcessID(54321, 0); err != nil {
			t.Fatal(err)
		}
		if state.pid.Load() != 54321 {
			t.Fatal("actual browser PID was not recorded")
		}
		if err := state.recordBrowserProcessID(failure.pid, failure.hr); err == nil {
			t.Fatal("failed PID query succeeded")
		}
		if state.pid.Load() != 0 {
			t.Fatal("failed PID query retained a stale browser PID")
		}
	}
	if err := state.recordBrowserProcessID(9876, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := state.arguments(nil); err != nil {
		t.Fatal(err)
	}
	if state.pid.Load() != 0 {
		t.Fatal("a new browser initialization retained the previous PID")
	}
}

func TestUpdateBrowserSelectionCannotRacePastStartup(t *testing.T) {
	for range 32 {
		var state updateBrowserStartupState
		start := make(chan struct{})
		selection := make(chan error, 1)
		go func() {
			<-start
			selection <- state.enableLegacy()
		}()
		close(start)
		got, err := state.arguments([]string{"--disable-features=msSmartScreenProtection"})
		if err != nil {
			t.Fatal(err)
		}
		selected := <-selection == nil
		if selected != (got == "--disable-features=msSmartScreenProtection") {
			t.Fatalf("selection success %v disagrees with startup policy %q", selected, got)
		}
		if err := state.enableLegacy(); err == nil {
			t.Fatal("startup did not seal the policy")
		}
	}
}
