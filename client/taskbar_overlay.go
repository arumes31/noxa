package main

import (
	"sync"
	"sync/atomic"
	"time"
)

type taskbarOverlayState struct {
	hwnd       uintptr
	mode       trayIconState
	generation uint64
}

// One immutable latest request and one wake token bound memory even while the
// shell is busy. Only a state/window event retries a failed shell operation.
// No timer or per-frame work is involved.
type taskbarOverlayQueue struct {
	desired  atomic.Pointer[taskbarOverlayState]
	stopped  atomic.Bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newTaskbarOverlayQueue() *taskbarOverlayQueue {
	return &taskbarOverlayQueue{wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
}
func (q *taskbarOverlayQueue) signal() {
	if q.stopped.Load() {
		return
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (q *taskbarOverlayQueue) publish(hwnd uintptr, mode trayIconState) {
	for !q.stopped.Load() {
		old := q.desired.Load()
		if old != nil && old.hwnd == hwnd && old.mode == mode {
			return
		}
		next := &taskbarOverlayState{hwnd: hwnd, mode: mode}
		if old != nil {
			next.generation = old.generation
			if old.hwnd != hwnd {
				next.generation++
			}
		}
		if q.desired.CompareAndSwap(old, next) {
			q.signal()
			return
		}
	}
}

// Called by the exact-window hook. It performs no shell/COM call and never
// takes a lock held during native hook removal or worker shutdown.
func (q *taskbarOverlayQueue) windowEvent(hwnd uintptr, destroyed bool) {
	for !q.stopped.Load() {
		old := q.desired.Load()
		if old == nil || old.hwnd != hwnd {
			return
		}
		next := *old
		next.generation++
		if destroyed {
			next.hwnd = 0
		}
		if q.desired.CompareAndSwap(old, &next) {
			q.signal()
			return
		}
	}
}
func (q *taskbarOverlayQueue) run(apply func(taskbarOverlayState), cleanup func()) {
	defer close(q.done)
	defer cleanup()
	var attempted *taskbarOverlayState
	for {
		select {
		case <-q.stop:
			return
		case <-q.wake:
		}
		if q.stopped.Load() {
			return
		}
		next := q.desired.Load()
		if next == nil || next == attempted {
			continue
		}
		attempted = next
		apply(*next)
	}
}
func (q *taskbarOverlayQueue) stopNow() {
	q.stopOnce.Do(func() { q.stopped.Store(true); close(q.stop) })
}
func (q *taskbarOverlayQueue) wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-q.done:
		return true
	case <-timer.C:
		return false
	}
}
