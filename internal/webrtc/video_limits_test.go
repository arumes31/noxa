package webrtc

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestVideoBudgetAggregateAndLifecycle(t *testing.T) {
	r := NewRouter(nil)
	if err := r.SetVideoBitrateLimit(8000); err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	at := time.Unix(100, 0)
	if !r.allowVideoPacket("publisher", 600, at) || r.allowVideoPacket("publisher", 500, at) || !r.allowVideoPacket("publisher", 400, at) {
		t.Fatal("incorrect one-second burst")
	}
	r.JoinChannel(2, "publisher")
	r.DetachPeerKeepChannel("publisher")
	if r.allowVideoPacket("publisher", 1, at) {
		t.Fatal("movement or ICE rebuild refilled budget")
	}
	if !r.allowVideoPacket("publisher", 500, at.Add(500*time.Millisecond)) || r.allowVideoPacket("publisher", 1, at.Add(500*time.Millisecond)) {
		t.Fatal("wrong continuous refill")
	}
	if r.allowVideoPacket("publisher", 1, at) {
		t.Fatal("backward clock refilled budget")
	}
	if !r.allowVideoPacket("publisher", 1000, at.Add(time.Hour)) || r.allowVideoPacket("publisher", 1, at.Add(time.Hour)) {
		t.Fatal("idle burst exceeded one second")
	}
	if r.allowVideoPacket("unknown", 1, at) {
		t.Fatal("unjoined publisher obtained budget")
	}
	r.JoinChannel(2, "other")
	if !r.allowVideoPacket("other", 1000, at.Add(time.Hour)) || r.allowVideoPacket("other", 1, at.Add(time.Hour)) {
		t.Fatal("publishers do not have independent full budgets")
	}
	r.DetachPeer("other")
	r.DetachPeer("publisher")
	if len(r.videoBudgets) != 0 {
		t.Fatal("disconnected publisher budget retained")
	}
	if r.allowVideoPacket("publisher", 1, at) || len(r.videoBudgets) != 0 {
		t.Fatal("late packet recreated disconnected budget")
	}
	if err := r.SetVideoBitrateLimit(-1); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestVideoBitrateLimitBeforeFanoutAndRecording(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	for _, id := range []string{"viewer-a", "viewer-b"} {
		attachFakePeer(t, e, r, id)
		r.JoinChannel(1, id)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	testVideoPublication(t, r, "publisher", "viewer-a", SlotCam)
	testVideoPublication(t, r, "publisher", "viewer-b", SlotCam)
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	registerVideoSource(r, "publisher", SlotCam, "", 1)
	packet := &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: make([]byte, 988)}
	copy(packet.Payload, boundsKeyPacket(1, 1, 640, 360).Payload)
	if err := r.SetVideoBitrateLimit(8000); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0)
	if sent := r.forwardVideoAt("publisher", SlotCam, "", packet, at); sent != 3 || tap.count() != 1 {
		t.Fatalf("fan-out charged per recipient: %d tap=%d", sent, tap.count())
	}
	if r.allowVideoPacket("publisher", 1, at) {
		t.Fatal("RTP headers were not charged")
	}
	// Another source/layer of the same publisher must share the exhausted budget.
	r.SetTrackSlots("publisher", map[string]string{"camera": SlotCam, "display": SlotScreen})
	testVideoPublication(t, r, "publisher", "viewer-a", SlotScreen)
	testVideoPublication(t, r, "publisher", "viewer-b", SlotScreen)
	registerVideoSource(r, "publisher", SlotScreen, "f", 2)
	if sent := r.forwardVideoAt("publisher", SlotScreen, "f", packet, at); sent != 0 || tap.count() != 1 {
		t.Fatalf("slot/layer bypass: %d tap=%d", sent, tap.count())
	}
	if err := r.SetVideoBitrateLimit(0); err != nil {
		t.Fatal(err)
	}
	if sent := r.forwardVideoAt("publisher", SlotScreen, "f", packet, at); sent != 3 || tap.count() != 2 {
		t.Fatalf("default unlimited behavior: %d tap=%d", sent, tap.count())
	}
}

func TestVideoBudgetConcurrentLayersShareCapacity(t *testing.T) {
	r := NewRouter(nil)
	if err := r.SetVideoBitrateLimit(8000); err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if r.allowVideoPacket("publisher", 100, time.Unix(100, 0)) {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("concurrent tracks exceeded shared burst: %d", accepted.Load())
	}
	if err := r.SetVideoBitrateLimit(8000); err != nil {
		t.Fatal(err)
	}
	if r.allowVideoPacket("publisher", 1, time.Unix(100, 0)) {
		t.Fatal("reapplying unchanged limit restored budget")
	}
}
