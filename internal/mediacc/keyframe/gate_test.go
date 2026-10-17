package keyframe

import (
	"testing"
	"time"
)

func TestGateForwardsReceivableRequests(t *testing.T) {
	var g FeedbackGate
	now := time.Unix(100, 0)
	for i := range 20 {
		if !g.Allow(now.Add(time.Duration(i)*200*time.Millisecond), BacklogLimit) {
			t.Fatalf("request %d with a receivable backlog was deferred", i)
		}
	}
	if g.Deferred() != 0 {
		t.Fatal("deferred count without deferral")
	}
}

func TestGateBacksOffWhileBacklogged(t *testing.T) {
	var g FeedbackGate
	start := time.Unix(100, 0)
	var forwarded []time.Duration
	// A receiver repeating its request every 200 ms for a minute while its
	// relay queue stays backlogged.
	for at := time.Duration(0); at <= time.Minute; at += 200 * time.Millisecond {
		if g.Allow(start.Add(at), time.Second) {
			forwarded = append(forwarded, at)
		}
	}
	want := []time.Duration{0, 2 * time.Second, 6 * time.Second, 14 * time.Second, 30 * time.Second, 46 * time.Second}
	if len(forwarded) != len(want) {
		t.Fatalf("forwarded %v, want %v", forwarded, want)
	}
	for i := range want {
		if forwarded[i] != want[i] {
			t.Fatalf("forwarded %v, want %v", forwarded, want)
		}
	}
	if g.Deferred() == 0 {
		t.Fatal("deferrals not counted")
	}
}

func TestGateForwardsAtOnceWhenBacklogClears(t *testing.T) {
	var g FeedbackGate
	start := time.Unix(100, 0)
	if !g.Allow(start, time.Second) {
		t.Fatal("first request deferred")
	}
	if g.Allow(start.Add(500*time.Millisecond), time.Second) {
		t.Fatal("backlogged repeat forwarded inside the allowance")
	}
	if !g.Allow(start.Add(700*time.Millisecond), 100*time.Millisecond) {
		t.Fatal("request after the backlog cleared was deferred")
	}
	// The allowance restarts from the minimum after recovery.
	if !g.Allow(start.Add(3*time.Second), time.Second) || g.Allow(start.Add(4*time.Second), time.Second) {
		t.Fatal("allowance did not restart after recovery")
	}
	if !g.Allow(start.Add(5*time.Second), time.Second) {
		t.Fatal("minimum allowance not honoured after recovery")
	}
}
