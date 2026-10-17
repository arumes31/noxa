package webrtc

import (
	"testing"
	"time"

	"noxa/internal/mediacc/keyframe"
)

// backloggedPacer reports a fixed backlog without running a pacing worker.
func backloggedPacer(queuedBytes, bitrate int) *mediaPacer {
	return &mediaPacer{streams: make(map[uint32]*pacedStream), bitrate: bitrate,
		count: 1, bytes: queuedBytes, stop: make(chan struct{}), done: make(chan struct{})}
}

func TestPacerVideoBacklogUsesPacedRate(t *testing.T) {
	p := backloggedPacer(375_000, 2_000_000) // 375 kB at 1.5 * 2 Mbit/s
	if got := p.videoBacklog(); got != time.Second {
		t.Fatalf("backlog %v, want 1s", got)
	}
	p.count, p.bytes = 0, 0
	if p.videoBacklog() != 0 {
		t.Fatal("empty queue reported a backlog")
	}
}

// A receiver whose relay queue cannot deliver a keyframe promptly must not make
// the shared publisher encoder produce one every coalescing window; once its
// backlog clears, its next request is forwarded immediately.
func TestBackloggedReceiverKeyframeRequestsAreDeferred(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "viewer")
	r.JoinChannel(1, "viewer")
	r.JoinChannel(1, "pub")
	testVideoPublication(t, r, "pub", "viewer", SlotCam)
	registerVideoSource(r, "pub", SlotCam, "", 10)
	r.mu.Lock()
	if r.videoIngress == nil {
		r.videoIngress = make(map[videoIngressKey]*videoIngressDiagnostic)
	}
	r.videoIngress[videoIngressKey{"pub", SlotCam, ""}] = &videoIngressDiagnostic{ssrc: 10, started: time.Now()}
	r.mu.Unlock()
	output := pubTrackFor(r, "viewer", "pub").video[SlotCam]
	if output.egress == nil {
		output.egress = &mediaEgressStream{}
	}
	pacer := backloggedPacer(3_000_000, 4_000_000) // 4 s behind
	output.egress.mu.Lock()
	previous := output.egress.pacer
	output.egress.pacer = pacer
	output.egress.mu.Unlock()
	t.Cleanup(func() {
		output.egress.mu.Lock()
		output.egress.pacer = previous
		output.egress.mu.Unlock()
	})
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	openWindow := func() {
		r.mu.Lock()
		clear(r.keyframeLast)
		r.mu.Unlock()
	}

	// The first backlogged request is forwarded; immediate repeats are not,
	// even with the per-source coalescing window open.
	r.relayKeyframeRequest("viewer", output.sender)
	for range 5 {
		openWindow()
		r.relayKeyframeRequest("viewer", output.sender)
	}
	if feedback.pliCount() != 1 {
		t.Fatalf("backlogged receiver forced %d publisher keyframes, want 1", feedback.pliCount())
	}
	if output.keyframeGate.Deferred() != 5 {
		t.Fatalf("deferred %d requests, want 5", output.keyframeGate.Deferred())
	}
	r.mu.RLock()
	ingress := r.videoIngress[videoIngressKey{"pub", SlotCam, ""}]
	r.mu.RUnlock()
	if ingress == nil {
		t.Fatal("test source has no ingress diagnostics")
	}
	ingress.mu.Lock()
	snapshot := ingress.keyframeRequestSnapshot()
	ingress.mu.Unlock()
	if snapshot == nil || snapshot.ViewerFeedback.Deferred != 5 || snapshot.ViewerFeedback.WriteSucceeded != 1 {
		t.Fatalf("deferred requests not visible in diagnostics: %+v", snapshot)
	}

	// Backlog drained: the receiver's next request is served at once.
	pacer.mu.Lock()
	pacer.count, pacer.bytes = 0, 0
	pacer.mu.Unlock()
	openWindow()
	r.relayKeyframeRequest("viewer", output.sender)
	if feedback.pliCount() != 2 {
		t.Fatalf("request after the backlog cleared was not forwarded (%d)", feedback.pliCount())
	}
	if keyframe.BacklogLimit <= 0 {
		t.Fatal("invalid backlog limit")
	}
}
