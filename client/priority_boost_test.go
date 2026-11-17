package main

import (
	"slices"
	"testing"
)

func TestCaptureBoostHoldsUntilLastReasonEnds(t *testing.T) {
	var calls []bool
	b := captureBoost{apply: func(on bool) error { calls = append(calls, on); return nil }}
	b.set("native", false) // Never held: nothing to restore.
	b.set("browser", true)
	b.set("native", true)
	b.set("browser", false)
	b.set("native", false)
	b.set("native", false)
	if want := []bool{true, true, true, false}; !slices.Equal(calls, want) {
		t.Fatalf("boost calls = %v, want %v", calls, want)
	}
}

func TestDisplayCaptureLeaseDrivesBrowserBoost(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	var states []bool
	f.p.boost = func(on bool) { states = append(states, on) }
	id := acquireDisplayCapture(t, f.p)
	if len(states) == 0 || !states[len(states)-1] {
		t.Fatalf("lease did not boost: %v", states)
	}
	if err := f.p.release(id); err != nil {
		t.Fatal(err)
	}
	if states[len(states)-1] {
		t.Fatalf("release did not restore: %v", states)
	}
	acquireDisplayCapture(t, f.p)
	if err := f.p.shutdown(); err != nil {
		t.Fatal(err)
	}
	if states[len(states)-1] {
		t.Fatalf("shutdown did not restore: %v", states)
	}
}
