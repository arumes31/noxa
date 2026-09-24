package main

import (
	"reflect"
	"sync"
	"testing"

	"golang.design/x/hotkey"
)

type lifecycleHotkeyEvent struct {
	name string
	data any
}

type lifecycleHotkeyRecorder struct {
	mu     sync.Mutex
	events []lifecycleHotkeyEvent
}

func (r *lifecycleHotkeyRecorder) snapshot() []lifecycleHotkeyEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]lifecycleHotkeyEvent(nil), r.events...)
}

func newHotkeyLifecycleApp(t *testing.T) (*App, *lifecycleHotkeyRecorder) {
	t.Helper()
	recorder := &lifecycleHotkeyRecorder{}
	original := hotkeyEventEmitter
	hotkeyEventEmitter = func(_ *App, event string, data any) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		recorder.events = append(recorder.events, lifecycleHotkeyEvent{event, data})
	}
	t.Cleanup(func() { hotkeyEventEmitter = original })
	a := &App{hotkeys: map[string]*hotkeyReg{}}
	t.Cleanup(func() { a.shutdown(nil) })
	return a, recorder
}

// Real OS registration stays disabled by TestMain. Install the registration
// explicitly so tests can deliver callbacks in the problematic order without
// timers, scheduling assumptions, or changes to the user's global shortcuts.
func installLifecycleHotkey(a *App, action, spec string) (uint64, *hotkeyReg) {
	a.applyHotkey(action, spec)
	a.hkMu.Lock()
	defer a.hkMu.Unlock()
	reg := &hotkeyReg{cancel: make(chan struct{})}
	a.hotkeys[action] = reg
	return a.hotkeyGeneration[action], reg
}

func TestPendingHotkeyCannotInstallAfterUnbind(t *testing.T) {
	a := &App{hotkeys: map[string]*hotkeyReg{}}
	a.applyHotkey("ptt", "F8")
	generation := a.hotkeyGeneration["ptt"]
	a.applyHotkey("ptt", "")
	a.passiveHotkeyLoop("ptt", nil, hotkey.KeyF8, generation)
	if len(a.hotkeys) != 0 {
		t.Fatal("obsolete shortcut installed after unbind")
	}
}

func TestPendingHotkeyCannotInstallAfterShutdown(t *testing.T) {
	a := &App{hotkeys: map[string]*hotkeyReg{}}
	a.applyHotkey("ptt", "F8")
	generation := a.hotkeyGeneration["ptt"]
	a.shutdown(nil)
	a.passiveHotkeyLoop("ptt", nil, hotkey.KeyF8, generation)
	a.applyHotkey("ptt", "F9")
	if len(a.hotkeys) != 0 {
		t.Fatal("shortcut installed after shutdown")
	}
}

func TestSupersededHotkeyCallbacksCannotChangeReplacement(t *testing.T) {
	for _, action := range []string{"ptt", "mute_toggle"} {
		t.Run(action, func(t *testing.T) {
			a, recorder := newHotkeyLifecycleApp(t)
			oldGeneration, _ := installLifecycleHotkey(a, action, "F8")
			newGeneration, replacement := installLifecycleHotkey(a, action, "F9")
			a.publishHotkey(action, newGeneration, true)
			before := recorder.snapshot()
			// The cancelled worker can finish a key-state poll or deliver its
			// final release after the replacement key has already gone down.
			a.publishHotkey(action, oldGeneration, true)
			a.publishHotkey(action, oldGeneration, false)
			if got := recorder.snapshot(); !reflect.DeepEqual(got, before) {
				t.Fatalf("superseded callbacks published events: before=%v after=%v", before, got)
			}
			if action == "ptt" {
				a.hkMu.Lock()
				pressed := replacement.pressed
				a.hkMu.Unlock()
				if !pressed {
					t.Fatal("obsolete release cleared the held replacement PTT key")
				}
			}
		})
	}
}

func TestSupersededHotkeyStatusCannotOverwriteReplacement(t *testing.T) {
	a, recorder := newHotkeyLifecycleApp(t)
	oldGeneration, _ := installLifecycleHotkey(a, "ptt", "F8")
	newGeneration, _ := installLifecycleHotkey(a, "ptt", "F9")
	status := hotkeyStatus{Action: "ptt", Registered: true}
	a.publishHotkeyStatus(status, newGeneration)
	before := recorder.snapshot()
	if len(before) == 0 || before[len(before)-1].name != "hotkey_status" {
		t.Fatal("current shortcut status was not published")
	}
	a.publishHotkeyStatus(status, oldGeneration)
	a.publishHotkeyStatus(hotkeyStatus{Action: "ptt", Error: "late registration failure"}, oldGeneration)
	if got := recorder.snapshot(); !reflect.DeepEqual(got, before) {
		t.Fatalf("obsolete worker overwrote replacement status: before=%v after=%v", before, got)
	}
}

func TestHeldHotkeyReleasesBeforeReplacementBinding(t *testing.T) {
	a, recorder := newHotkeyLifecycleApp(t)
	oldGeneration, old := installLifecycleHotkey(a, "ptt", "F8")
	a.publishHotkey("ptt", oldGeneration, true)
	start := len(recorder.snapshot())
	newGeneration, _ := installLifecycleHotkey(a, "ptt", "F9")
	a.publishHotkey("ptt", newGeneration, true)
	a.publishHotkey("ptt", oldGeneration, false)
	want := []lifecycleHotkeyEvent{
		{"hotkey", "ptt_up"},
		{"hotkey_binding", map[string]string{"action": "ptt", "spec": "F9"}},
		{"hotkey", "ptt_down"},
	}
	if got := recorder.snapshot()[start:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("held-key replacement event order = %v, want %v", got, want)
	}
	select {
	case <-old.cancel:
	default:
		t.Fatal("replaced worker was not cancelled")
	}
}

func TestHotkeyEventsSuppressedAfterShutdown(t *testing.T) {
	a, recorder := newHotkeyLifecycleApp(t)
	generation, _ := installLifecycleHotkey(a, "ptt", "F8")
	a.publishHotkey("ptt", generation, true)
	a.shutdown(nil)
	before := recorder.snapshot()
	a.publishHotkey("ptt", generation, true)
	a.publishHotkey("ptt", generation, false)
	a.publishHotkeyStatus(hotkeyStatus{Action: "ptt", Registered: true}, generation)
	a.applyHotkey("ptt", "F9")
	if got := recorder.snapshot(); !reflect.DeepEqual(got, before) {
		t.Fatalf("shortcut worker published after shutdown: before=%v after=%v", before, got)
	}
}
