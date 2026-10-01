package webrtc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCommitVideoLimitsAbandonsStalledOutput(t *testing.T) {
	v := runtimeVoice(t)
	v.JoinChannel("publisher", 1)
	v.JoinChannel("recorder", 1)
	w := &heldVideoWriter{make(chan struct{}), make(chan struct{})}
	v.router.addVideoOutput("recorder", w)
	done := make(chan int, 1)
	go func() { done <- v.router.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(1, 1, 640, 360)) }()
	var release sync.Once
	defer release.Do(func() { close(w.release) })
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("output did not start")
	}
	api, policy := v.engine.api, v.router.videoPolicySnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	called := false
	err := v.CommitVideoLimits(ctx, 8000, VideoBounds{640, 360}, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("stalled output: error=%v persisted=%v", err, called)
	}
	if v.engine.api != api || v.router.videoPolicySnapshot() != policy {
		t.Fatal("timed-out update changed runtime configuration")
	}
	release.Do(func() { close(w.release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("output did not finish")
	}
	if err := v.SetVideoLimits(16000, VideoBounds{1280, 720}); err != nil {
		t.Fatal(err)
	}
	if v.engine.videoBounds != (VideoBounds{1280, 720}) || v.router.videoPolicySnapshot().bounds != v.engine.videoBounds {
		t.Fatal("abandoned waiter changed a later update")
	}
}

func TestCommitVideoLimitsRejectsInvalidOrClosedBeforePersistence(t *testing.T) {
	v := runtimeVoice(t)
	persist := func(context.Context) error { t.Error("unexpected persistence"); return nil }
	if err := v.CommitVideoLimits(t.Context(), -1, VideoBounds{}, persist); err == nil {
		t.Fatal("accepted invalid rate")
	}
	if err := v.CommitVideoLimits(t.Context(), 0, VideoBounds{640, 0}, persist); err == nil {
		t.Fatal("accepted invalid dimensions")
	}
	if err := v.engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.CommitVideoLimits(t.Context(), 0, VideoBounds{}, persist); err == nil {
		t.Fatal("accepted closed engine")
	}
}

func TestCommitVideoLimitsPersistenceOutcome(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed despite cancellation", true: "failed"}[fails], func(t *testing.T) {
			v := runtimeVoice(t)
			oldBounds, newBounds := VideoBounds{640, 360}, VideoBounds{1280, 720}
			if err := v.SetVideoLimits(800, oldBounds); err != nil {
				t.Fatal(err)
			}
			v.JoinChannel("publisher", 1)
			at := time.Unix(100, 0)
			if !v.router.allowVideoPacket("publisher", 100, at) {
				t.Fatal("initial budget unavailable")
			}
			api, policy := v.engine.api, v.router.videoPolicySnapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("persistence failed")
			calls := 0
			err := v.CommitVideoLimits(ctx, 1600, newBounds, func(context.Context) error {
				calls++
				if v.engine.api != api || v.router.videoPolicy != policy || v.router.videoBitrateLimit != 800 {
					t.Error("runtime configuration changed before commit")
				}
				cancel()
				if fails {
					return failure
				}
				return nil
			})
			if calls != 1 {
				t.Fatalf("persistence calls=%d", calls)
			}
			if fails {
				if !errors.Is(err, failure) || v.engine.api != api || v.router.videoPolicySnapshot() != policy || v.router.allowVideoPacket("publisher", 1, at) {
					t.Fatal("failed persistence changed configuration or budget")
				}
			} else if err != nil || v.engine.videoBounds != newBounds || v.router.videoPolicySnapshot().bounds != newBounds || !v.router.allowVideoPacket("publisher", 200, at) {
				t.Fatalf("confirmed commit not installed: %v", err)
			}
		})
	}
}

func TestCommitVideoLimitsCancelsLockWaits(t *testing.T) {
	for _, target := range []string{"update", "engine"} {
		t.Run(target, func(t *testing.T) {
			v := runtimeVoice(t)
			if target == "update" {
				v.videoLimitsMu.Lock()
				defer v.videoLimitsMu.Unlock()
			} else {
				v.engine.mu.Lock()
				defer v.engine.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			err := v.CommitVideoLimits(ctx, 800, VideoBounds{640, 360}, func(context.Context) error {
				t.Error("persisted while waiting for lock")
				return nil
			})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCommitVideoLimitsGatesPeersAndRejectsEarlierInspection(t *testing.T) {
	v := runtimeVoice(t)
	v.JoinChannel("publisher", 1)
	v.JoinChannel("recorder", 1)
	w := &fakeTrackWriter{}
	v.router.addVideoOutput("recorder", w)
	inspected, write := make(chan struct{}), make(chan struct{})
	var releaseWrite sync.Once
	defer releaseWrite.Do(func() { close(write) })
	v.SetMediaGuard(func(_ MediaDelivery, deliver func() error) error {
		close(inspected)
		<-write
		return deliver()
	})
	forwarded := make(chan int, 1)
	go func() { forwarded <- v.router.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(1, 1, 1280, 720)) }()
	select {
	case <-inspected:
	case <-time.After(time.Second):
		t.Fatal("packet did not reach guard")
	}
	persisting, finish := make(chan struct{}), make(chan struct{})
	var releaseCommit sync.Once
	defer releaseCommit.Do(func() { close(finish) })
	committed := make(chan error, 1)
	go func() {
		committed <- v.CommitVideoLimits(t.Context(), 8000, VideoBounds{640, 360}, func(ctx context.Context) error {
			close(persisting)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-persisting:
	case <-time.After(time.Second):
		t.Fatal("persistence did not start")
	}
	releaseWrite.Do(func() { close(write) })
	peer := make(chan *PeerConnectionWrapper, 1)
	go func() {
		p, err := v.engine.NewPeerConnection("new-peer")
		if err != nil {
			t.Error(err)
		}
		peer <- p
	}()
	waitPendingVideoWriter(t, &v.engine.mu)
	// Busy policy commits drop packets immediately; they must never reach
	// the recording writer before or after the commit.
	select {
	case sent := <-forwarded:
		if sent != 0 || w.count() != 0 {
			t.Fatal("old packet passed the persistence gate")
		}
		forwarded <- sent
	case <-peer:
		t.Fatal("peer created during persistence")
	case <-time.After(10 * time.Millisecond):
	}
	releaseCommit.Do(func() { close(finish) })
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if sent := <-forwarded; sent != 0 || w.count() != 0 {
		t.Fatal("earlier inspection crossed the committed policy")
	}
	if p := <-peer; p == nil || p.videoBounds != (VideoBounds{640, 360}) {
		t.Fatal("queued peer used old codec configuration")
	}
}
