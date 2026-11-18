package main

import (
	"bytes"
	"errors"
	"log"
	"slices"
	"strings"
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

func TestDisplayCaptureLeaseDrivesCaptureBoost(t *testing.T) {
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

// A failure repeated by the 15 s refresh is logged once per process.
func TestCaptureBoostLogsEachFailureOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })
	log.SetOutput(&buf)
	failure := errors.Join(errors.New("D3DKMTGetProcessSchedulingPriorityClass pid 7: NTSTATUS 0xC0000022"), errors.New("OpenProcess pid 9: Access is denied."))
	b := captureBoost{apply: func(bool) error { return failure }}
	for range 3 {
		b.set("browser", true)
	}
	if got := strings.Count(buf.String(), "\n"); got != 2 {
		t.Fatalf("logged %d lines:\n%s", got, buf.String())
	}
}
