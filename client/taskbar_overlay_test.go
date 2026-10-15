package main

import (
	"testing"
	"time"
)

func overlayReceive(t *testing.T, c <-chan taskbarOverlayState) taskbarOverlayState {
	t.Helper()
	select {
	case s := <-c:
		return s
	case <-time.After(time.Second):
		t.Fatal("overlay worker did not apply")
		return taskbarOverlayState{}
	}
}
func overlayQuiet(t *testing.T, c <-chan taskbarOverlayState) {
	t.Helper()
	select {
	case s := <-c:
		t.Fatalf("unexpected overlay work: %+v", s)
	case <-time.After(20 * time.Millisecond):
	}
}
func TestTaskbarOverlayCoalescesAndDeduplicates(t *testing.T) {
	q := newTaskbarOverlayQueue()
	calls := make(chan taskbarOverlayState, 10)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	go q.run(func(s taskbarOverlayState) {
		calls <- s
		if s.mode == trayIdle {
			close(entered)
			<-unblock
		}
	}, func() {})
	defer func() {
		q.stopNow()
		if !q.wait(time.Second) {
			t.Error("worker did not close")
		}
	}()
	q.publish(9, trayIdle)
	<-entered
	first := overlayReceive(t, calls)
	for i := 0; i < 100; i++ {
		q.publish(9, trayTalking)
		q.publish(9, trayMicMuted)
	}
	q.publish(9, trayBothMuted)
	close(unblock)
	last := overlayReceive(t, calls)
	if last.mode != trayBothMuted || last.hwnd != 9 || last.generation != first.generation {
		t.Fatalf("lost final state or recreated shell generation: %+v", last)
	}
	q.publish(9, trayBothMuted)
	overlayQuiet(t, calls)
	q.windowEvent(8, false)
	overlayQuiet(t, calls)
	q.windowEvent(9, false)
	restart := overlayReceive(t, calls)
	if restart.generation != last.generation+1 || restart.mode != last.mode {
		t.Fatal("Explorer restart lost state")
	}
}
func TestTaskbarOverlayFailureRetriesOnlyOnEvent(t *testing.T) {
	q := newTaskbarOverlayQueue()
	calls := make(chan taskbarOverlayState, 10)
	// An unsuccessful native apply returns; no retry loop is allowed.
	go q.run(func(s taskbarOverlayState) { calls <- s }, func() {})
	defer func() { q.stopNow(); q.wait(time.Second) }()
	q.publish(3, trayDeafened)
	overlayReceive(t, calls)
	q.signal()
	q.publish(3, trayDeafened)
	overlayQuiet(t, calls)
	q.windowEvent(3, false)
	overlayReceive(t, calls)
	q.windowEvent(3, true)
	dead := overlayReceive(t, calls)
	if dead.hwnd != 0 {
		t.Fatal("destroy did not invalidate target")
	}
	q.windowEvent(3, false)
	overlayQuiet(t, calls)
	q.publish(4, trayDeafened)
	newWindow := overlayReceive(t, calls)
	if newWindow.hwnd != 4 || newWindow.generation <= dead.generation {
		t.Fatal("replacement window lost identity")
	}
}
func TestTaskbarOverlayShutdownRetainsBusyOwner(t *testing.T) {
	q := newTaskbarOverlayQueue()
	entered := make(chan struct{})
	unblock := make(chan struct{})
	closed := make(chan struct{})
	go q.run(func(taskbarOverlayState) { close(entered); <-unblock }, func() { close(closed) })
	q.publish(1, trayIdle)
	<-entered
	q.stopNow()
	if q.wait(time.Millisecond) {
		t.Fatal("hung apply was falsely joined")
	}
	before := q.desired.Load()
	q.publish(2, trayTalking)
	q.windowEvent(1, false)
	if q.desired.Load() != before {
		t.Fatal("shutdown allowed new work")
	}
	close(unblock)
	if !q.wait(time.Second) {
		t.Fatal("released owner did not exit")
	}
	select {
	case <-closed:
	default:
		t.Fatal("COM cleanup missing")
	}
}
