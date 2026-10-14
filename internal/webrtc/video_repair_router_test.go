package webrtc

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type adaptiveRepairRouterPeer struct {
	fakeRTCPWriter
	rtt     time.Duration
	queries int
}

func (p *adaptiveRepairRouterPeer) RequestVideoRepairRTT() *videoRepairRTTResult {
	p.queries++
	r := &videoRepairRTTResult{done: make(chan struct{}), rtt: p.rtt, measuredAt: time.Now(), requestedAt: time.Now()}
	close(r.done)
	return r
}

type adaptiveRepairRouterTrack struct{ repairPollingTrack }

func (*adaptiveRepairRouterTrack) Codec() pion.RTPCodecParameters {
	return pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42001f"}}
}

type adaptiveRepairRouterOutput struct {
	fakeTrackWriter
	at []time.Time
}

func (w *adaptiveRepairRouterOutput) WriteRTP(p *rtp.Packet) error {
	w.at = append(w.at, time.Now())
	return w.fakeTrackWriter.WriteRTP(p)
}

// Exercise the production router's publisher-provider wiring and downstream
// H264 assembly. A newer intact keyframe remains safe after gap abandonment.
func TestRouterUsesPublisherRepairRTTWithoutChangingH264Safety(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rtt, want time.Duration
	}{
		{"fresh low RTT", 40 * time.Millisecond, 280 * time.Millisecond},
		{"unknown RTT", 0, videoReorderWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := NewRouter(nil)
				r.JoinChannel(1, "publisher")
				r.JoinChannel(1, "recorder")
				r.SetTrackSlots("publisher", map[string]string{"desktop": SlotScreen})
				out := &adaptiveRepairRouterOutput{}
				r.addVideoOutput("recorder", out)
				peer := &adaptiveRepairRouterPeer{rtt: tc.rtt}
				r.rtcpWriters["publisher"] = peer
				key := func(seq uint16, timestamp uint32) *rtp.Packet {
					return h264ForwardPacket(seq, timestamp, true, 24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1)
				}
				track := &adaptiveRepairRouterTrack{repairPollingTrack{initial: []*rtp.Packet{key(1, 90), key(3, 180)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}}
				start := time.Now()
				go func() { time.Sleep(3 * time.Second); close(track.closed) }()
				r.ReadVideoLoop("publisher", SlotScreen, track)
				if peer.queries != 1 || len(out.at) != 2 || out.at[0] != start || out.at[1].Sub(start) != tc.want {
					t.Fatalf("queries=%d forwarded=%v expected second at %v", peer.queries, out.at, tc.want)
				}
			})
		})
	}
}
