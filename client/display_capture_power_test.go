package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeDisplayCapturePower struct {
	events        []string
	next          uintptr
	open          map[uintptr]bool
	flags         map[uintptr]map[uint32]bool
	failCreate    bool
	failState     bool
	powerEpoch    uint64
	powerBlocked  bool
	backendClosed bool
	onSet         func(uint32)
	failSet       *uint32
	failClear     bool
	createStart   chan struct{}
	createGo      chan struct{}
}

func (b *fakeDisplayCapturePower) create() (uintptr, error) {
	b.events = append(b.events, "create")
	if b.createStart != nil {
		close(b.createStart)
		<-b.createGo
	}
	if b.failCreate {
		return 0, errors.New("create failed")
	}
	if b.open == nil {
		b.open = make(map[uintptr]bool)
		b.flags = make(map[uintptr]map[uint32]bool)
	}
	b.next++
	b.open[b.next] = true
	b.flags[b.next] = make(map[uint32]bool)
	return b.next, nil
}
func (b *fakeDisplayCapturePower) set(handle uintptr, kind uint32) error {
	b.events = append(b.events, fmt.Sprintf("set:%d", kind))
	if b.failSet != nil && *b.failSet == kind {
		return errors.New("set failed")
	}
	b.flags[handle][kind] = true
	if b.onSet != nil {
		b.onSet(kind)
	}
	return nil
}
func (b *fakeDisplayCapturePower) clear(handle uintptr, kind uint32) error {
	b.events = append(b.events, fmt.Sprintf("clear:%d", kind))
	if b.failClear {
		return errors.New("clear failed")
	}
	delete(b.flags[handle], kind)
	return nil
}
func (b *fakeDisplayCapturePower) close(handle uintptr) error {
	b.events = append(b.events, "close")
	delete(b.open, handle)
	delete(b.flags, handle)
	return nil
}

type fakeDisplayCaptureTimer struct {
	delay     time.Duration
	callback  func()
	cancelled bool
}
type displayCaptureFixture struct {
	p       *displayCapturePower
	backend *fakeDisplayCapturePower
	now     time.Time
	timers  []*fakeDisplayCaptureTimer
}

func newDisplayCaptureFixture(t *testing.T) *displayCaptureFixture {
	t.Helper()
	f := &displayCaptureFixture{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		backend: &fakeDisplayCapturePower{}}
	f.p = &displayCapturePower{backend: f.backend, now: func() time.Time { return f.now },
		schedule: func(delay time.Duration, callback func()) func() {
			timer := &fakeDisplayCaptureTimer{delay: delay, callback: callback}
			f.timers = append(f.timers, timer)
			return func() { timer.cancelled = true }
		}}
	t.Cleanup(func() {
		_ = f.p.shutdown()
		if len(f.backend.open) != 0 {
			t.Errorf("native handles leaked: %v", f.backend.open)
		}
	})
	return f
}
func acquireDisplayCapture(t *testing.T, p *displayCapturePower) string {
	t.Helper()
	id, err := p.acquire()
	if err != nil || id == "" {
		t.Fatalf("acquire: id=%q error=%v", id, err)
	}
	return id
}

func TestDisplayCaptureLeasesShareRequestUntilFinalRelease(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	first := acquireDisplayCapture(t, f.p)
	second := acquireDisplayCapture(t, f.p)
	if first == second || len(f.backend.open) != 1 || len(f.p.leases) != 2 {
		t.Fatal("captures did not share one handle with distinct leases")
	}
	want := []string{"create", "set:1", "set:0"}
	if !reflect.DeepEqual(f.backend.events, want) {
		t.Fatalf("unexpected native requests: %v", f.backend.events)
	}
	if err := f.p.release(first); err != nil || len(f.backend.open) != 1 {
		t.Fatal("first release removed another capture's protection")
	}
	if err := f.p.release(first); err != nil || len(f.backend.open) != 1 {
		t.Fatal("duplicate release changed remaining lease")
	}
	if err := f.p.release(second); err != nil || len(f.backend.open) != 0 || f.p.timer != nil {
		t.Fatal("final release retained power request or timer")
	}
	want = append(want, "clear:0", "clear:1", "close")
	if !reflect.DeepEqual(f.backend.events, want) {
		t.Fatalf("unbalanced lifecycle: %v", f.backend.events)
	}
	before := len(f.backend.events)
	if err := f.p.release(second); err != nil || len(f.backend.events) != before {
		t.Fatal("released handle was closed twice")
	}
}

func TestDisplayCaptureExpiryIsPerLeaseAndRefreshCannotResurrect(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	first := acquireDisplayCapture(t, f.p)
	f.now = f.now.Add(30 * time.Second)
	second := acquireDisplayCapture(t, f.p)
	f.now = f.now.Add(90 * time.Second) // First lease is exactly expired.
	if err := f.p.refresh(first); err == nil {
		t.Fatal("expired lease was renewed")
	}
	if len(f.p.leases) != 1 || len(f.backend.open) != 1 {
		t.Fatal("expiry affected a later capture")
	}
	if err := f.p.refresh(second); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(displayCaptureLeaseTTL)
	if err := f.p.refresh(second); err == nil || len(f.backend.open) != 0 || len(f.p.leases) != 0 {
		t.Fatal("exact-deadline expiry did not close final handle")
	}
	if err := f.p.release(first); err != nil {
		t.Fatal(err)
	}
	fresh := acquireDisplayCapture(t, f.p)
	if fresh == first || fresh == second || f.backend.next != 2 {
		t.Fatal("fresh acquisition reused an expired lease or handle")
	}
}

func TestDisplayCaptureExpiryTimerClosesAbandonedCapture(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	acquireDisplayCapture(t, f.p)
	timer := f.timers[len(f.timers)-1]
	if timer.delay != displayCaptureLeaseTTL {
		t.Fatalf("expiry delay = %v", timer.delay)
	}
	f.now = f.now.Add(displayCaptureLeaseTTL)
	timer.callback()
	if len(f.backend.open) != 0 || len(f.p.leases) != 0 || f.p.timer != nil {
		t.Fatal("abandoned renderer retained protection")
	}
}

func TestDisplayCaptureStaleTimerCannotExpireRenewedCapture(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	id := acquireDisplayCapture(t, f.p)
	old := f.timers[len(f.timers)-1]
	f.now = f.now.Add(15 * time.Second)
	if err := f.p.refresh(id); err != nil {
		t.Fatal(err)
	}
	current := f.timers[len(f.timers)-1]
	if !old.cancelled || current.delay != displayCaptureLeaseTTL {
		t.Fatal("refresh did not replace expiry timer")
	}
	f.now = f.now.Add(105 * time.Second)
	old.callback() // Simulate Stop racing a previously scheduled callback.
	if len(f.backend.open) != 1 || current.cancelled {
		t.Fatal("stale callback affected current lease")
	}
	f.now = f.now.Add(15 * time.Second)
	current.callback()
	if len(f.backend.open) != 0 {
		t.Fatal("renewed deadline did not expire")
	}
}

func TestDisplayCaptureAcquisitionRollsBackNativeFailures(t *testing.T) {
	for _, stage := range []string{"create", "system", "display"} {
		t.Run(stage, func(t *testing.T) {
			f := newDisplayCaptureFixture(t)
			want := []string{"create"}
			switch stage {
			case "create":
				f.backend.failCreate = true
			case "system":
				kind := displayCaptureSystemRequired
				f.backend.failSet = &kind
				want = append(want, "set:1", "close")
			case "display":
				kind := displayCaptureDisplayRequired
				f.backend.failSet = &kind
				want = append(want, "set:1", "set:0", "clear:1", "close")
			}
			id, err := f.p.acquire()
			if err == nil || id != "" || len(f.p.leases) != 0 || len(f.backend.open) != 0 || f.p.timer != nil {
				t.Fatal("failed acquisition retained a lease, handle or timer")
			}
			if !reflect.DeepEqual(f.backend.events, want) {
				t.Fatalf("cleanup = %v; want %v", f.backend.events, want)
			}
			f.backend.failCreate, f.backend.failSet = false, nil
			acquireDisplayCapture(t, f.p)
		})
	}
}

func TestDisplayCaptureClearFailureStillClosesHandle(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	id := acquireDisplayCapture(t, f.p)
	f.backend.failClear = true
	if err := f.p.release(id); err == nil || len(f.backend.open) != 0 || f.p.handle != 0 {
		t.Fatal("clear failure leaked handle or was silently hidden")
	}
}

func TestDisplayCaptureLeaseCountIsBoundedAndUnknownIDsDoNotAllocate(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	if err := f.p.refresh("unknown"); err == nil {
		t.Fatal("unknown refresh succeeded")
	}
	if err := f.p.release("unknown"); err != nil || len(f.backend.events) != 0 || len(f.timers) != 0 {
		t.Fatal("unknown release created native work")
	}
	for i := 0; i < maxDisplayCaptureLeases; i++ {
		acquireDisplayCapture(t, f.p)
	}
	before := len(f.backend.events)
	if id, err := f.p.acquire(); err == nil || id != "" || len(f.backend.events) != before {
		t.Fatal("lease limit did not reject without native work")
	}
}

func TestDisplayCaptureShutdownRejectsLateWorkAndTimers(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	id := acquireDisplayCapture(t, f.p)
	timer := f.timers[len(f.timers)-1]
	if err := f.p.shutdown(); err != nil || len(f.backend.open) != 0 || !timer.cancelled {
		t.Fatal("shutdown retained active request")
	}
	before := len(f.backend.events)
	if _, err := f.p.acquire(); err == nil {
		t.Fatal("acquisition after shutdown succeeded")
	}
	if err := f.p.refresh(id); err == nil {
		t.Fatal("refresh after shutdown succeeded")
	}
	if err := f.p.release(id); err != nil {
		t.Fatal(err)
	}
	timer.callback()
	if err := f.p.shutdown(); err != nil || len(f.backend.events) != before {
		t.Fatal("late work resurrected or double-closed request")
	}
}

func TestDisplayCaptureShutdownRacingAcquisitionClosesResult(t *testing.T) {
	backend := &fakeDisplayCapturePower{createStart: make(chan struct{}), createGo: make(chan struct{})}
	p := &displayCapturePower{backend: backend, schedule: func(time.Duration, func()) func() { return func() {} }}
	acquired := make(chan error, 1)
	go func() { _, err := p.acquire(); acquired <- err }()
	<-backend.createStart
	shutting := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(shutting); done <- p.shutdown() }()
	<-shutting
	close(backend.createGo)
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(backend.open) != 0 || len(p.leases) != 0 || !p.closed {
		t.Fatal("shutdown lost concurrent acquisition")
	}
}

func TestDisplayCaptureConcurrentLeaseCleanup(t *testing.T) {
	backend := &fakeDisplayCapturePower{}
	p := &displayCapturePower{backend: backend, schedule: func(time.Duration, func()) func() { return func() {} }}
	var wait sync.WaitGroup
	for i := 0; i < maxDisplayCaptureLeases; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id, err := p.acquire()
			if err != nil {
				t.Error(err)
				return
			}
			if err := p.refresh(id); err != nil {
				t.Error(err)
			}
			if err := p.release(id); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if err := p.shutdown(); err != nil || len(backend.open) != 0 {
		t.Fatal("concurrent lifecycle leaked")
	}
}

func TestDisplayCaptureAppBindingsAndShutdown(t *testing.T) {
	backend := &fakeDisplayCapturePower{}
	app := &App{displayCapturePower: displayCapturePower{backend: backend,
		schedule: func(time.Duration, func()) func() { return func() {} }}}
	id, err := app.AcquireDisplayCaptureLease()
	if err != nil || id == "" {
		t.Fatalf("acquire: %v", err)
	}
	if err := app.RefreshDisplayCaptureLease(id); err != nil {
		t.Fatal(err)
	}
	app.shutdown(context.Background())
	if len(backend.open) != 0 {
		t.Fatal("App shutdown did not close power request")
	}
	if _, err := app.AcquireDisplayCaptureLease(); err == nil {
		t.Fatal("closed App accepted new capture")
	}
	if err := app.ReleaseDisplayCaptureLease(id); err != nil {
		t.Fatal(err)
	}
}

func (b *fakeDisplayCapturePower) state() (uint64, bool, error) {
	if b.failState {
		return 0, false, errors.New("notification registration failed")
	}
	return b.powerEpoch, b.powerBlocked, nil
}
func (b *fakeDisplayCapturePower) shutdown() error { b.backendClosed = true; return nil }

func TestDisplayCaptureShortSleepInvalidatesLeaseAndAutomaticResumeStaysBlocked(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	old := acquireDisplayCapture(t, f.p)
	f.now = f.now.Add(5 * time.Second)
	f.backend.powerEpoch, f.backend.powerBlocked = 1, true
	if err := f.p.refresh(old); err == nil || len(f.backend.open) != 0 || len(f.p.leases) != 0 {
		t.Fatal("short suspend kept pre-sleep lease")
	}
	if _, err := f.p.acquire(); err == nil {
		t.Fatal("acquire while suspended succeeded")
	}
	f.backend.powerEpoch = 2 // automatic resume is not interactive permission.
	if _, err := f.p.acquire(); err == nil || len(f.backend.open) != 0 {
		t.Fatal("automatic resume woke display")
	}
	f.backend.powerEpoch, f.backend.powerBlocked = 3, false
	fresh := acquireDisplayCapture(t, f.p)
	if fresh == old {
		t.Fatal("interactive resume reused stale lease")
	}
	if err := f.p.refresh(old); err == nil || len(f.backend.open) != 1 {
		t.Fatal("old refresh affected resumed capture")
	}
	if err := f.p.release(old); err != nil || len(f.backend.open) != 1 {
		t.Fatal("old release affected resumed capture")
	}
}
func TestDisplayCaptureMissedSuspendBetweenRefreshesStillInvalidates(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	old := acquireDisplayCapture(t, f.p)
	f.backend.powerEpoch = 3 // suspend and both resume events occurred between reads.
	if err := f.p.refresh(old); err == nil || len(f.backend.open) != 0 {
		t.Fatal("short completed sleep retained obsolete power request")
	}
	acquireDisplayCapture(t, f.p)
}
func TestDisplayCaptureNotificationFailureDoesNotAcquirePower(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	f.backend.failState = true
	if _, err := f.p.acquire(); err == nil || len(f.backend.events) != 0 || len(f.p.leases) != 0 {
		t.Fatal("unobservable resume still acquired power")
	}
}

func TestDisplayCapturePowerTransitionDuringAcquisitionRollsBack(t *testing.T) {
	for _, scenario := range []string{"suspend", "short-complete-sleep", "state-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDisplayCaptureFixture(t)
			f.backend.onSet = func(kind uint32) {
				if kind != displayCaptureDisplayRequired {
					return
				}
				switch scenario {
				case "suspend":
					f.backend.powerEpoch, f.backend.powerBlocked = 1, true
				case "short-complete-sleep":
					f.backend.powerEpoch = 3
				case "state-failure":
					f.backend.failState = true
				}
			}
			id, err := f.p.acquire()
			if err == nil || id != "" || len(f.backend.open) != 0 || len(f.p.leases) != 0 || f.p.timer != nil {
				t.Fatal("acquisition crossing power transition leaked or returned a lease")
			}
		})
	}
}

func TestDisplayCaptureStateFailureOnRefreshClosesProtection(t *testing.T) {
	f := newDisplayCaptureFixture(t)
	id := acquireDisplayCapture(t, f.p)
	f.backend.failState = true
	if err := f.p.refresh(id); err == nil || len(f.backend.open) != 0 || len(f.p.leases) != 0 {
		t.Fatal("unobservable power state retained active lease")
	}
}

func TestDisplayCaptureIdleAppNeverInitializesPowerBackend(t *testing.T) {
	app := &App{}
	app.shutdown(context.Background())
	if app.displayCapturePower.backend != nil || app.displayCapturePower.timer != nil {
		t.Fatal("idle App initialized native power observation")
	}
}
