// voice_stability_test.go holds deterministic regression tests for the
// per-pair voice failures found by the chaos test (voice_chaos_test.go): one
// listener permanently unable to hear one speaker while every other pair works.
package webrtc

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

func answerOffer(t *testing.T, client *pion.PeerConnection, offer string) string {
	t.Helper()
	if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer}); err != nil {
		t.Fatalf("client SetRemoteDescription(offer): %v", err)
	}
	answer, err := client.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("client CreateAnswer: %v", err)
	}
	if err := client.SetLocalDescription(answer); err != nil {
		t.Fatalf("client SetLocalDescription: %v", err)
	}
	return answer.SDP
}

func stabilityVoice(t *testing.T) (*Voice, *manualRenegClock) {
	t.Helper()
	e, err := New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	v := NewVoice(e, NewRouter(nil), zap.NewNop())
	clock := newManualRenegClock(time.Unix(0, 0))
	useManualRenegClock(v, clock, 50*time.Millisecond, 300*time.Millisecond)
	v.renegAnswer = time.Second
	return v, clock
}

// A server offer the frontend never answers (lost on the native event bridge,
// dropped during a tab switch, or answered with an error) used to wedge the
// peer in have-local-offer forever: every member who joined afterwards stayed
// inaudible to that listener. Pion cannot roll back a local offer, so the
// outstanding offer is redelivered until it is answered.
func TestUnansweredOfferIsRedelivered(t *testing.T) {
	v, clock := stabilityVoice(t)
	t.Cleanup(func() { _ = v.ClosePeer("listener") })
	client := newClientPC(t)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, client, "listener")
	v.JoinChannel("listener", 1)
	v.JoinChannel("first", 1)
	clock.Advance(50 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("offers = %d, want 1", rec.count())
	}
	// The offer is lost. A later joiner must still reach the listener.
	v.JoinChannel("late", 1)
	clock.Advance(300 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("offered again while previous offer outstanding: %d", rec.count())
	}
	clock.Advance(time.Second) // answer watchdog
	if rec.count() != 2 || rec.sdp(1) != rec.sdp(0) {
		t.Fatalf("outstanding offer not redelivered: %d offers", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(1))); err != nil {
		t.Fatalf("answer to redelivered offer: %v", err)
	}
	clock.Advance(300 * time.Millisecond)
	if rec.count() != 3 || !strings.Contains(rec.sdp(2), " late\r\n") {
		t.Fatalf("pending late joiner not offered after redelivery: %d offers", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(2))); err != nil {
		t.Fatal(err)
	}
	if state := v.engine.PeerConnection("listener").pc.SignalingState(); state != pion.SignalingStateStable {
		t.Fatalf("signaling state = %s", state)
	}
}

// Redelivery backs off and never re-announces a publisher that left (or was
// hidden from the listener) after the offer was created.
func TestRedeliveredOfferDropsDepartedPublishers(t *testing.T) {
	v, clock := stabilityVoice(t)
	t.Cleanup(func() { _ = v.ClosePeer("listener") })
	client := newClientPC(t)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, client, "listener")
	v.JoinChannel("listener", 1)
	v.JoinChannel("stays", 1)
	v.JoinChannel("leaves", 1)
	clock.Advance(50 * time.Millisecond)
	if rec.count() != 1 || !strings.Contains(rec.sdp(0), "a=msid:noxa-leaves leaves") {
		t.Fatalf("fixture offer missing: %d", rec.count())
	}
	v.LeaveChannel("leaves", 1)
	clock.Advance(time.Second)
	if rec.count() != 2 {
		t.Fatalf("offers = %d, want redelivery", rec.count())
	}
	if strings.Contains(rec.sdp(1), "noxa-leaves") || !strings.Contains(rec.sdp(1), "a=msid:noxa-stays stays") {
		t.Fatal("redelivered offer announces a departed publisher or lost a current one")
	}
	// Backoff: the next redelivery waits twice as long.
	clock.Advance(time.Second)
	if rec.count() != 2 {
		t.Fatal("redelivery did not back off")
	}
	clock.Advance(time.Second)
	if rec.count() != 3 {
		t.Fatalf("second redelivery missing: %d", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(2))); err != nil {
		t.Fatalf("answer to sanitized offer: %v", err)
	}
	clock.Advance(300 * time.Millisecond)
	if rec.count() != 4 || strings.Contains(rec.sdp(3), "noxa-leaves") {
		t.Fatalf("follow-up offer after sanitized redelivery: %d", rec.count())
	}
}

// An offer that was applied locally but failed delivery (control write
// error) used to leave the peer in have-local-offer with nothing scheduled.
func TestUndeliveredOfferIsRetried(t *testing.T) {
	v, clock := stabilityVoice(t)
	t.Cleanup(func() { _ = v.ClosePeer("listener") })
	client := newClientPC(t)
	rec := &offerRecorder{}
	var fail atomic.Bool
	fail.Store(true)
	v.SetOfferSender(func(_ string, sdp string) error {
		if fail.Load() {
			return errors.New("control connection write failed")
		}
		rec.add(sdp)
		return nil
	})
	establishVoiceSession(t, v, client, "listener")
	v.JoinChannel("listener", 1)
	v.JoinChannel("speaker", 1)
	clock.Advance(50 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatal("fixture delivered an offer")
	}
	fail.Store(false)
	clock.Advance(300 * time.Millisecond)
	if rec.count() != 1 || !strings.Contains(rec.sdp(0), " speaker\r\n") {
		t.Fatalf("undelivered change was not retried: %d offers", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(0))); err != nil {
		t.Fatal(err)
	}
}

// A client offer colliding with a server offer the client evidently never
// received makes the server redeliver it at once, so the client's collision
// path (answer the crossing offer, then retry) can complete.
func TestStaleCollisionRedeliversServerOffer(t *testing.T) {
	v, clock := stabilityVoice(t)
	v.renegAnswer = time.Hour // isolate the collision path from the watchdog
	t.Cleanup(func() { _ = v.ClosePeer("listener") })
	client := newClientPC(t)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, client, "listener")
	v.JoinChannel("listener", 1)
	v.JoinChannel("speaker", 1)
	clock.Advance(50 * time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("offers = %d", rec.count())
	}
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh server offer may still be in flight: plain collision.
	if _, err := v.HandleOffer("listener", offer.SDP, nil); !errors.Is(err, ErrOfferCollision) {
		t.Fatalf("fresh collision = %v, want ErrOfferCollision", err)
	}
	clock.Advance(time.Nanosecond)
	if rec.count() != 1 {
		t.Fatal("fresh collision redelivered the offer")
	}
	clock.Advance(renegStaleOffer)
	if _, err := v.HandleOffer("listener", offer.SDP, nil); !errors.Is(err, ErrOfferCollision) {
		t.Fatalf("stale collision = %v, want ErrOfferCollision", err)
	}
	clock.Advance(time.Nanosecond)
	if rec.count() != 2 || rec.sdp(1) != rec.sdp(0) {
		t.Fatalf("stale collision did not redeliver the offer: %d", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(1))); err != nil {
		t.Fatal(err)
	}
	establishVoiceSession(t, v, client, "listener") // the client's retry now succeeds
}

// An answer that cannot be applied must not leave the offer outstanding
// without another chance.
func TestRejectedAnswerRedeliversOffer(t *testing.T) {
	v, clock := stabilityVoice(t)
	t.Cleanup(func() { _ = v.ClosePeer("listener") })
	client := newClientPC(t)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, sdp string) error { rec.add(sdp); return nil })
	establishVoiceSession(t, v, client, "listener")
	v.JoinChannel("listener", 1)
	v.JoinChannel("speaker", 1)
	clock.Advance(50 * time.Millisecond)
	if err := v.HandleAnswer("listener", "v=0\r\nnot an answer"); err == nil {
		t.Fatal("malformed answer accepted")
	}
	clock.Advance(time.Nanosecond)
	if rec.count() != 2 {
		t.Fatalf("offers after rejected answer = %d, want 2", rec.count())
	}
	if err := v.HandleAnswer("listener", answerOffer(t, client, rec.sdp(1))); err != nil {
		t.Fatal(err)
	}
}

// Guard refreshes run concurrently with joins. A refresh that pruned a pair
// (stale snapshot) used to neither renegotiate nor restore it: the listener's
// SDP kept announcing a speaker whose sender had been removed.
func TestPublisherGuardRestoresPrunedPairsAndRenegotiates(t *testing.T) {
	e, err := New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "listener")
	attachFakePeer(t, e, r, "speaker")
	r.JoinChannel(1, "listener")
	r.JoinChannel(1, "speaker")
	var renegotiated []string
	r.SetRenegotiateHook(func(sub string) { renegotiated = append(renegotiated, sub) })
	r.SetPublisherGuard(func(pair PublisherAccess) bool { return pair.PublisherID != "speaker" })
	if pubTrackFor(r, "listener", "speaker") != nil {
		t.Fatal("stale guard did not prune")
	}
	if !containsString(renegotiated, "listener") {
		t.Fatalf("pruned pair was not renegotiated: %v", renegotiated)
	}
	renegotiated = nil
	r.SetPublisherGuard(func(PublisherAccess) bool { return true })
	if pubTrackFor(r, "listener", "speaker") == nil {
		t.Fatal("fresh guard did not restore the pruned pair")
	}
	if !containsString(renegotiated, "listener") {
		t.Fatalf("restored pair was not renegotiated: %v", renegotiated)
	}
	renegotiated = nil
	r.SetPublisherGuard(func(PublisherAccess) bool { return true })
	if len(renegotiated) != 0 {
		t.Fatalf("unchanged guard renegotiated: %v", renegotiated)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// A stopped egress stream on a still-wanted pair is rebuilt at the next SDP
// preparation instead of staying announced and silent.
func TestPrepareSubscriberRebuildsStoppedAudioOutput(t *testing.T) {
	e, err := New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	for _, id := range []string{"listener", "speaker"} {
		pc, err := e.NewPeerConnection(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.AttachPeer(id, pc); err != nil {
			t.Fatal(err)
		}
	}
	r.JoinChannel(1, "listener")
	r.JoinChannel(1, "speaker")
	old := pubTrackFor(r, "listener", "speaker").audio[SlotMic]
	if old == nil || old.egress == nil {
		t.Fatal("fixture lacks a guarded mic output")
	}
	if r.PrepareSubscriber("listener") {
		t.Fatal("healthy subscriber reported changes")
	}
	old.egress.stop()
	if !r.PrepareSubscriber("listener") {
		t.Fatal("stopped output not reported as changed")
	}
	rebuilt := pubTrackFor(r, "listener", "speaker").audio[SlotMic]
	if rebuilt == nil || rebuilt == old || !rebuilt.egress.Active() {
		t.Fatal("stopped mic output was not rebuilt")
	}
}

// ---------------------------------------------------------------------------
// Real-transport reproductions (loopback ICE/DTLS/SRTP via the chaos harness)
// ---------------------------------------------------------------------------

func quietChaosConfig(clients int) chaosConfig {
	cfg := defaultChaosConfig()
	cfg.clients = clients
	cfg.loss, cfg.jitter, cfg.socketFault, cfg.offerDrop = 0, 0, 0, 0
	cfg.bound = 8 * time.Second
	return cfg
}

func joinAll(h *chaosHarness, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "p" + string(rune('a'+i))
	}
	h.parallel(n, func(i int) { h.join(ids[i]) })
	return ids
}

// The original symptom: a short burst of socket write errors on the server
// (an ICE restart closing the muxed socket under the in-flight packet, or
// ENOBUFS) permanently retired whichever (listener, speaker) outputs happened
// to write during it.
func TestVoiceChaosSocketOutageHeals(t *testing.T) {
	h := newChaosHarness(t, quietChaosConfig(4), 7)
	joinAll(h, 4)
	h.verify("before outage")
	var outage atomic.Bool
	outage.Store(true)
	fault := func(datagram []byte) error {
		if outage.Load() && isSRTPDatagram(datagram) {
			h.faults.Add(1)
			return io.ErrClosedPipe
		}
		return nil
	}
	mediaSocketFault.Store(&fault)
	time.Sleep(400 * time.Millisecond)
	outage.Store(false)
	if h.faults.Load() == 0 {
		t.Fatal("outage injected no faults")
	}
	h.verify("after outage")
}

// Reconnecting with the same client ID (new peer connection and DTLS
// identity, channel membership kept) must restore every pair both ways.
func TestVoiceChaosReconnectSameClient(t *testing.T) {
	h := newChaosHarness(t, quietChaosConfig(4), 3)
	ids := joinAll(h, 4)
	h.verify("before reconnects")
	for i := 0; i < 6; i++ {
		h.reconnect(ids[i%len(ids)], false)
		h.verify(fmt.Sprintf("after reconnect %d of %s", i, ids[i%len(ids)]))
	}
}

// In-place ICE restarts over the shared UDP mux keep every pair alive.
func TestVoiceChaosICERestarts(t *testing.T) {
	h := newChaosHarness(t, quietChaosConfig(3), 9)
	ids := joinAll(h, 3)
	h.verify("before restarts")
	for i := 0; i < 6; i++ {
		h.client(ids[i%len(ids)]).reoffer(true)
		h.verify(fmt.Sprintf("after ICE restart %d of %s", i, ids[i%len(ids)]))
	}
}

// A speaker who leaves and rejoins while the listeners' offers are lost: the
// rejoin reuses the listener's old transceiver for the new track while the
// outstanding offer still describes it as unused.
func TestVoiceChaosRejoinDuringLostOffer(t *testing.T) {
	h := newChaosHarness(t, quietChaosConfig(3), 5)
	joinAll(h, 3)
	h.verify("before")
	h.dropOffers.Store(true)
	h.leave("pa")
	time.Sleep(200 * time.Millisecond)
	h.join("pa")
	time.Sleep(100 * time.Millisecond)
	h.dropOffers.Store(false)
	h.verify("after rejoin during lost offer")
}

// Offers lost on the way to the frontend during a join burst must not keep
// the late joiners inaudible.
func TestVoiceChaosLostOffersHeal(t *testing.T) {
	cfg := quietChaosConfig(5)
	h := newChaosHarness(t, cfg, 11)
	ids := joinAll(h, 4)
	h.verify("before lost offers")
	h.dropOffers.Store(true) // every server offer of the next join is lost
	h.join("late")
	time.Sleep(300 * time.Millisecond)
	h.dropOffers.Store(false)
	_ = ids
	h.verify("after lost offers")
}
