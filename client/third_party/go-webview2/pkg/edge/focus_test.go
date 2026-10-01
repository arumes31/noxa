//go:build windows

package edge

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFocus(t *testing.T) {
	if code := os.Getenv("NOXA_FOCUS_TEST"); code != "" {
		result := uintptr(0)
		if code == "invalid" {
			result = uintptr(windows.E_INVALIDARG)
		}
		if code == "fatal" {
			result = uintptr(windows.E_FAIL)
		}
		called := false
		e := NewChromium()
		e.controller = &ICoreWebView2Controller{vtbl: &_ICoreWebView2ControllerVtbl{
			MoveFocus: NewComProc(func(_ uintptr, reason uintptr) uintptr {
				called = true
				if reason != uintptr(COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC) {
					return uintptr(windows.E_FAIL)
				}
				return result
			}),
		}}
		e.Focus()
		if !called {
			t.Fatal("focus was never attempted")
		}
		return
	}
	for _, code := range []string{"success", "invalid", "fatal"} {
		t.Run(code, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestFocus$")
			cmd.Env = append(os.Environ(), "NOXA_FOCUS_TEST="+code)
			output, err := cmd.CombinedOutput()
			if code == "fatal" {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("other HRESULT must remain fatal: %v %s", err, output)
				}
			} else if err != nil {
				t.Fatalf("focus terminated the client: %v %s", err, output)
			}
		})
	}
}

func TestFocusWithoutUsableController(t *testing.T) {
	NewChromium().Focus()
	e := NewChromium()
	e.controller = &ICoreWebView2Controller{}
	e.shuttingDown = true
	e.Focus()
	e.shuttingDown = false
	e.hwnd = ^uintptr(0) // An invalid/disabled parent cannot receive focus.
	e.Focus()
}
