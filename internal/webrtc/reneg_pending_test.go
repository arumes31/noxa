package webrtc

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestRenegotiationRetainsChangesUntilPreviousOfferAnswered(t *testing.T) {
	const debounce = 50 * time.Millisecond
	const rateLimit = 300 * time.Millisecond
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	v := NewVoice(e, r, testLogger())
	clock := newManualRenegClock(time.Unix(0, 0))
	useManualRenegClock(v, clock, debounce, rateLimit)
	defer func() { _ = v.ClosePeer("subscriber") }()
	client := newClientPC(t)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, client, "subscriber")
	r.JoinChannel(1, "subscriber")
	r.JoinChannel(1, "first-publisher")
	clock.Advance(debounce)
	if rec.count() != 1 {
		t.Fatal("first offer was not emitted")
	}
	// A later track change cannot be included in the offer already in flight.
	r.JoinChannel(1, "late-publisher")
	clock.Advance(rateLimit)
	if rec.count() != 1 {
		t.Fatal("sent overlapping offers while awaiting an answer")
	}
	if got := clock.Pending(); got != 0 {
		t.Fatalf("retry timers while awaiting an answer = %d, want 0", got)
	}
	if err := v.HandleAnswer("subscriber", "invalid SDP"); err == nil {
		t.Fatal("invalid answer unexpectedly accepted")
	}
	clock.Advance(10 * rateLimit)
	if rec.count() != 1 || clock.Pending() != 0 {
		t.Fatal("invalid answer resumed negotiation or started retry timers")
	}
	if err := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: rec.sdp(0)}); err != nil {
		t.Fatal(err)
	}
	answer, err := client.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	if err := v.HandleAnswer("subscriber", answer.SDP); err != nil {
		t.Fatal(err)
	}
	// No new membership event occurs. The pending late track must recover on
	// this answer, rather than waiting indefinitely for an unrelated join.
	clock.Advance(rateLimit)
	if got := rec.count(); got != 2 {
		t.Fatalf("offers after late answer = %d, want 2; pending publisher change was lost", got)
	}
	if !strings.Contains(rec.sdp(1), "late-publisher") {
		t.Fatal("resumed offer omitted the pending publisher")
	}
}

func TestRenegotiationChangesDuringOfferCreation(t *testing.T) {
	for _, name := range []string{"track change", "peer replacement"} {
		t.Run(name, func(t *testing.T) {
			const debounce = 50 * time.Millisecond
			const rateLimit = 300 * time.Millisecond
			started, release := make(chan struct{}), make(chan struct{})
			var blocked atomic.Bool
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			logger := testLogger().WithOptions(zap.Hooks(func(entry zapcore.Entry) error {
				if entry.Message == "webrtc renegotiation offer created" && blocked.CompareAndSwap(false, true) {
					close(started)
					<-release
				}
				return nil
			}))
			e, err := New(logger, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()
			r := NewRouter(nil)
			v := NewVoice(e, r, testLogger())
			clock := newManualRenegClock(time.Unix(0, 0))
			useManualRenegClock(v, clock, debounce, rateLimit)
			defer func() { _ = v.ClosePeer("subscriber") }()
			rec := &offerRecorder{}
			v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
			client := newClientPC(t)
			establishVoiceSession(t, v, client, "subscriber")
			r.JoinChannel(1, "subscriber")
			r.JoinChannel(1, "publisher")
			done := make(chan struct{})
			go func() {
				clock.Advance(debounce)
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("offer creation did not reach barrier")
			}
			if name == "peer replacement" {
				if err := v.ClosePeer("subscriber"); err != nil {
					t.Fatal(err)
				}
				establishVoiceSession(t, v, newClientPC(t), "subscriber")
				r.JoinChannel(1, "subscriber")
				unblock()
				<-done
				if rec.count() != 0 {
					t.Fatal("in-flight old offer was delivered to replacement peer")
				}
				clock.Advance(debounce)
				if rec.count() != 1 {
					t.Fatal("replacement peer did not receive its own offer")
				}
				return
			}
			r.JoinChannel(1, "late-publisher")
			clock.Advance(rateLimit)
			unblock()
			<-done
			if rec.count() != 1 {
				t.Fatal("initial offer was not delivered")
			}
			if err := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: rec.sdp(0)}); err != nil {
				t.Fatal(err)
			}
			answer, err := client.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SetLocalDescription(answer); err != nil {
				t.Fatal(err)
			}
			if err := v.HandleAnswer("subscriber", answer.SDP); err != nil {
				t.Fatal(err)
			}
			clock.Advance(rateLimit)
			if rec.count() != 2 || !strings.Contains(rec.sdp(1), "late-publisher") {
				t.Fatal("track change during offer creation was lost")
			}
		})
	}
}

func TestRenegotiationStaleTimerCannotOfferForReplacementPeer(t *testing.T) {
	const debounce = 50 * time.Millisecond
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	v := NewVoice(e, r, testLogger())
	clock := newManualRenegClock(time.Unix(0, 0))
	useManualRenegClock(v, clock, debounce, 300*time.Millisecond)
	defer func() { _ = v.ClosePeer("subscriber") }()
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, newClientPC(t), "subscriber")
	r.JoinChannel(1, "subscriber")
	r.JoinChannel(1, "publisher")
	clock.mu.Lock()
	staleCallback := clock.timers[len(clock.timers)-1].callback
	clock.mu.Unlock()
	if err := v.ClosePeer("subscriber"); err != nil {
		t.Fatal(err)
	}
	if got := clock.Pending(); got != 0 {
		t.Fatalf("timers after close = %d, want 0", got)
	}
	establishVoiceSession(t, v, newClientPC(t), "subscriber")
	r.JoinChannel(1, "subscriber")
	// A stopped AfterFunc callback may already be executing. Deliver it after
	// replacement to ensure the old session cannot consume the new work.
	staleCallback()
	if rec.count() != 0 {
		t.Fatal("old session's timer emitted an offer for its replacement")
	}
	clock.Advance(debounce)
	if got := rec.count(); got != 1 {
		t.Fatalf("replacement session offers = %d, want 1", got)
	}
}
