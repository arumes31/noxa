package webrtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestVideoStartupUsesCurrentAuthorizedWatchedIngress(t *testing.T) {
	r := NewRouter(nil)
	r.videoIngress = make(map[videoIngressKey]*videoIngressDiagnostic)
	for _, client := range []string{"pub", "camera", "not-watched", "viewer"} {
		r.JoinChannel(1, client)
	}
	now := time.Now()
	for i, client := range []string{"pub", "camera", "not-watched"} {
		generation, err := r.PublishVideo(client, SlotScreen, 0, true)
		if err != nil {
			t.Fatal(err)
		}
		if client != "not-watched" {
			if _, err := r.WatchVideo("viewer", client, SlotScreen, generation, 1, r.VideoWatchSession("viewer"), true); err != nil {
				t.Fatal(err)
			}
		}
		ssrc := uint32(i + 1)
		registerVideoSource(r, client, SlotScreen, "", ssrc)
		input := &videoIngressDiagnostic{ssrc: ssrc, started: now.Add(-time.Second)}
		input.stage.observe(&rtp.Header{Timestamp: 1}, 500_000, true, now.Add(-time.Second))
		input.stage.observe(&rtp.Header{Timestamp: 2}, 500_000, true, now)
		r.videoIngress[videoIngressKey{client, SlotScreen, ""}] = input
	}
	delivery := MediaDelivery{SenderID: "pub", RecipientID: "viewer", Slot: SlotScreen, ChannelID: 1, RecipientChannelID: 1}
	r.captureWatch(&delivery)
	if rate, allowed := r.videoStartupDemand(delivery, now); !allowed || rate != 16_000_000 {
		t.Fatalf("demand=%d allowed=%v, want only the two watched 8 Mbps sources", rate, allowed)
	}
	if rate, allowed := r.videoStartupDemand(delivery, now.Add(3*time.Second)); !allowed || rate != 0 {
		t.Fatalf("stale ingress became startup demand: %d allowed=%v", rate, allowed)
	}
	r.RevokeVideo("pub", SlotScreen)
	if _, allowed := r.videoStartupDemand(delivery, now); allowed {
		t.Fatal("retired publication could initiate a probe")
	}
}

func TestVideoStartupRequiresTWCCAndCannotResetOnAnotherWatch(t *testing.T) {
	p := newMediaPacer()
	defer func() { _ = p.Close() }()
	var calls atomic.Int32
	p.startVideoProbe = func(demand int) bool {
		calls.Add(1)
		p.SetTargetBitrate(demand) // Callback must run outside the pacing lock.
		return true
	}
	sink := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil })
	p.AddStream(1, sink)
	if p.startVideo(1, 20_000_000) || p.videoStarted.Load() {
		t.Fatal("stream without TWCC consumed the probe")
	}
	p.mu.Lock()
	p.streams[1].twccID = 3
	p.streams[1].audio = true
	p.mu.Unlock()
	if p.startVideo(1, 20_000_000) || p.videoStarted.Load() {
		t.Fatal("voice consumed the video probe")
	}
	p.mu.Lock()
	p.streams[1].audio = false
	p.mu.Unlock()
	if !p.startVideo(1, 20_000_000) || calls.Load() != 1 {
		t.Fatal("first video did not initiate its demand-sized probe")
	}
	p.SetTargetBitrate(2_000_000) // Later congestion must not be reset by a watch.
	p.AddStream(2, sink)
	p.mu.Lock()
	p.streams[2].twccID = 3
	p.mu.Unlock()
	if p.startVideo(2, 40_000_000) || calls.Load() != 1 || p.diagnosticSnapshot(time.Now()).TargetBitrateBPS != 2_000_000 {
		t.Fatal("later video overrode an established congestion estimate")
	}
}
