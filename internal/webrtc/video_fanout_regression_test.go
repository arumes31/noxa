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
	r.mu.RLock()
	size := len(r.keyframeLast)
	r.mu.RUnlock()
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
	r.mu.RLock()
	_, retained := r.keyframeLast[77]
	r.mu.RUnlock()
	if retained {
		t.Fatal("ended video reader retained keyframe throttle state")
	}

	registerVideoSource(r, "pub", SlotScreen, "", 78)
	r.RequestKeyframe("pub", SlotScreen, "")
	r.DetachPeer("pub")
	r.mu.RLock()
	size := len(r.keyframeLast)
	r.mu.RUnlock()
	if size != 0 || feedback.pliCount() != 2 {
		t.Fatalf("detached publisher retained %d throttle entries (PLIs %d)", size, feedback.pliCount())
	}
}
