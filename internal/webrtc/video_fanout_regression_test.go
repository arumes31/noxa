package webrtc

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Every publisher restart or codec rebind brings a new random SSRC. The PLI
// throttle must not retain one entry per SSRC for the server lifetime.
func TestKeyframeThrottleStateIsBounded(t *testing.T) {
	r := NewRouter(nil)
	now := time.Now()
	for i := range 10_000 {
		if !r.allowKeyframeRequest(uint32(i+1), now.Add(time.Duration(i)*100*time.Millisecond)) {
			t.Fatalf("first request for ssrc %d was throttled", i+1)
		}
	}
	r.keyframeMu.Lock()
	size := len(r.keyframeLast)
	r.keyframeMu.Unlock()
	if size > keyframeSweepThreshold+int(keyframeRequestWindow/(100*time.Millisecond)) {
		t.Fatalf("throttle map retained %d entries", size)
	}
	// The throttle itself still holds within its window.
	last := now.Add(9_999 * 100 * time.Millisecond)
	if r.allowKeyframeRequest(10_000, last.Add(keyframeRequestWindow/2)) {
		t.Fatal("pruning lifted the throttle of a live source")
	}
}

// An ended source (reader exit or publisher detach) forgets its throttle.
func TestKeyframeThrottleForgetsEndedSources(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	feedback := &fakeRTCPWriter{}
	r.mu.Lock()
	r.rtcpWriters["pub"] = feedback
	r.mu.Unlock()
	track := &fakeVideoTrack{ssrc: 77}
	track.packets = []*rtp.Packet{{Header: rtp.Header{Version: 2, SSRC: 77, SequenceNumber: 1}, Payload: []byte{1}}}
	registerVideoSource(r, "pub", SlotScreen, "", 77)
	r.RequestKeyframe("pub", SlotScreen, "")
	r.mu.Lock()
	delete(r.videoSources, "pub")
	r.mu.Unlock()
	r.ReadVideoLoop("pub", SlotScreen, track) // registers and unregisters 77
	r.keyframeMu.Lock()
	_, retained := r.keyframeLast[77]
	r.keyframeMu.Unlock()
	if retained {
		t.Fatal("ended video reader retained keyframe throttle state")
	}

	registerVideoSource(r, "pub", SlotScreen, "", 78)
	r.RequestKeyframe("pub", SlotScreen, "")
	r.DetachPeer("pub")
	r.keyframeMu.Lock()
	size := len(r.keyframeLast)
	r.keyframeMu.Unlock()
	if size != 0 || feedback.pliCount() != 2 {
		t.Fatalf("detached publisher retained %d throttle entries (PLIs %d)", size, feedback.pliCount())
	}
}

// A final write that meets a pending control update must not be discarded:
// its sequence number is already assigned, so dropping it leaves every
// recipient a hole. The pacer retries it after the update.
func TestPacerRetriesFinalWriteBehindControlUpdate(t *testing.T) {
	r := NewRouter(nil)
	ticket := mediaTicket{router: r, videoRevision: r.videoPolicySnapshot().revision, delivery: MediaDelivery{Slot: SlotCam, Tap: true}}
	p, _, packet, writes := terminalDiagnosticFixture(t, ticket)
	p.bitrate, p.stop, p.done = mediaInitialBitrate, make(chan struct{}), make(chan struct{})
	r.echoScopeMu.Lock() // a membership refresh holds the echo gate
	go p.run()
	defer func() { _ = p.Close() }()
	if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	// Each pacer tick pops the packet, attempts the gated write and requeues
	// it, so a single snapshot can land while it is in flight (count 0).
	// Retained means: never dropped, never written, and back in the queue.
	var queued, dropped int
	var written int64
	for held := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		queued, dropped, written = p.count, int(p.diagnostics.droppedScope), writes.Load()
		p.mu.Unlock()
		if dropped != 0 || written != 0 || queued == 1 || time.Now().After(held) {
			break
		}
	}
	if queued != 1 || dropped != 0 || written != 0 {
		r.echoScopeMu.Unlock()
		t.Fatalf("contended final write: queued=%d dropped=%d written=%d, want retained", queued, dropped, written)
	}
	r.echoScopeMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		count := p.count
		p.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retried packet was never written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.mu.Lock()
	written = writes.Load()
	p.mu.Unlock()
	if written != 1 {
		t.Fatalf("packet written %d times after the update", written)
	}
}

// Joining and leaving outside the echo channel must not take the echo gate:
// a pending writer there fails every recipient's final media write.
func TestMembershipRefreshSkipsUnchangedEchoGate(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "a")
	r.echoScopeMu.RLock() // an in-flight final write
	done := make(chan struct{})
	go func() {
		r.JoinChannel(1, "b")
		r.LeaveChannel(1, "b")
		close(done)
	}()
	select {
	case <-done:
		r.echoScopeMu.RUnlock()
	case <-time.After(time.Second):
		r.echoScopeMu.RUnlock()
		<-done
		t.Fatal("membership change waited for the echo gate without an echo channel")
	}
	r.SetEchoChannel(9)
	r.JoinChannel(9, "echo")
	r.echoScopeMu.RLock()
	defer r.echoScopeMu.RUnlock()
	if r.echoMembers["echo"] == 0 {
		t.Fatal("echo membership change was not recorded")
	}
}

// Binding notifications without a pending startup must not take the watch
// writer gate.
func TestWatchStartupFlushWithoutPendingSkipsWatchGate(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "viewer")
	r.JoinChannel(1, "viewer")
	r.mu.RLock()
	peer := r.pubPeers["viewer"]
	r.mu.RUnlock()
	r.watchMu.RLock()
	done := make(chan struct{})
	go func() { r.flushWatchStartup("viewer", peer); close(done) }()
	select {
	case <-done:
		r.watchMu.RUnlock()
	case <-time.After(time.Second):
		r.watchMu.RUnlock()
		<-done
		t.Fatal("startup flush took the watch writer gate with nothing pending")
	}
}

// The media-path throttle never takes Router.mu: a viewer awaiting a keyframe
// consults it for every rejected packet, and a queued Router.mu writer would
// block voice's TryRLock.
func TestKeyframeThrottleDoesNotTakeRouterLock(t *testing.T) {
	r := NewRouter(nil)
	r.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- r.allowKeyframeRequest(5, time.Now()) }()
	select {
	case ok := <-done:
		r.mu.Unlock()
		if !ok {
			t.Fatal("first request was throttled")
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		<-done
		t.Fatal("keyframe throttle waited for Router.mu")
	}
}
