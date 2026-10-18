package webrtc

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	pion "github.com/pion/webrtc/v4"
)

// countingRTCPWriter records every upstream RTCP packet by type.
type countingRTCPWriter struct {
	mu    sync.Mutex
	plis  int
	nacks int
	other int
}

func (w *countingRTCPWriter) WriteRTCP(packets []rtcp.Packet) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, packet := range packets {
		switch packet.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			w.plis++
		case *rtcp.TransportLayerNack:
			w.nacks++
		default:
			w.other++
		}
	}
	return nil
}

func (w *countingRTCPWriter) counts() (plis, nacks int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.plis, w.nacks
}

// videoLoopbackFixture is one production recipient peer over real
// ICE/DTLS/SRTP, watching publisher "pub" (whose RTCP goes to upstream).
type videoLoopbackFixture struct {
	router   *Router
	peer     *PeerConnectionWrapper
	receiver *pion.PeerConnection
	upstream *countingRTCPWriter
	output   *pubSlot
}

func newVideoLoopbackFixture(t *testing.T, slot string) *videoLoopbackFixture {
	t.Helper()
	engine, err := NewWithNetwork(testLogger(), []string{"stun:127.0.0.1:9"}, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	peer, err := engine.NewPeerConnection("viewer")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	if slot == SlotScreen {
		r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	}
	if err := r.AttachPeer("viewer", peer); err != nil {
		t.Fatal(err)
	}
	upstream := &countingRTCPWriter{}
	r.mu.Lock()
	r.rtcpWriters["pub"] = upstream
	r.mu.Unlock()
	r.PrepareSubscriber("viewer")
	f := &videoLoopbackFixture{router: r, peer: peer, upstream: upstream}
	f.receiver = newStartupTestReceiver(t)
	return f
}

func (f *videoLoopbackFixture) connect(t *testing.T, slot string) {
	t.Helper()
	negotiateStartupTestPeer(t, f.peer, f.receiver)
	f.output = pubTrackFor(f.router, "viewer", "pub").video[slot]
	deadline := time.Now().Add(5 * time.Second)
	for !f.output.videoTransportReady(f.router, "viewer") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !f.output.videoTransportReady(f.router, "viewer") {
		t.Fatal("recipient transport did not become ready")
	}
}

// A viewer's NACK for a packet sent about two seconds earlier, after 2.5 s
// worth of 15 Mbit/s packet count, is answered from the server's own send
// history. Nothing is requested from the publisher.
func TestViewerNACKAnsweredFromEgressHistory(t *testing.T) {
	f := newVideoLoopbackFixture(t, SlotCam)
	testVideoPublication(t, f.router, "pub", "viewer", SlotCam)
	registerVideoSource(f.router, "pub", SlotCam, "", 900)
	type arrival struct {
		seq uint16
		at  time.Time
	}
	arrivals := make(chan arrival, 16384)
	f.receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case arrivals <- arrival{packet.SequenceNumber, time.Now()}:
			default:
			}
		}
	})
	f.connect(t, SlotCam)
	// 2.5 s of 15 Mbit/s at ~1200-byte packets is ~3,900 packets. Small
	// payloads keep the pacer far below its budget; the history is counted
	// in packets, and the packets are spread over two seconds of wall time.
	const total = 3900
	start := time.Now()
	for i := range total {
		packet := continuityPacket(900, uint16(1000+i), uint32(i)*1500, uint16(i), true)
		packet.Version, packet.PayloadType, packet.Marker = 2, 96, true
		if f.router.ForwardVideo("pub", SlotCam, "", packet) != 1 {
			t.Fatalf("packet %d not forwarded", i)
		}
		if i%20 == 19 {
			time.Sleep(time.Until(start.Add(time.Duration(i) * 2 * time.Second / total)))
		}
	}
	// The test is about the repair path, not loopback delivery: a slow
	// (-race, 2-core) host may lose some of the burst. The target is the
	// first packet the viewer saw; every later forward still went through
	// the egress, so it sits about total positions deep in the history no
	// matter how many of the other packets reached the receiver.
	received := map[uint16]struct{}{}
	var target, newest uint16
	var targetAt time.Time
	targetSet := false
	collect := time.After(3 * time.Second)
collecting:
	for len(received) < total {
		select {
		case got := <-arrivals:
			received[got.seq] = struct{}{}
			if !targetSet {
				target, newest, targetAt, targetSet = got.seq, got.seq, got.at, true
			}
			if got.seq-target > newest-target && got.seq-target < 1<<15 {
				newest = got.seq
			}
		case <-collect:
			break collecting
		}
	}
	if !targetSet {
		t.Fatal("viewer received no packets")
	}
	if len(received) < total {
		t.Logf("loopback delivered %d of %d packets; repair is still checked", len(received), total)
	}
	if depth := newest - target; int(depth) < total*3/4 {
		t.Fatalf("target packet %d is only %d positions deep", target, depth)
	}
	if age := time.Since(targetAt); age < 1500*time.Millisecond {
		t.Fatalf("target packet is only %v old", age)
	}
	plisBefore, _ := f.upstream.counts()
	ssrc := f.output.egress.outputSSRC.Load()
	if err := f.receiver.WriteRTCP([]rtcp.Packet{&rtcp.TransportLayerNack{MediaSSRC: ssrc, Nacks: []rtcp.NackPair{{PacketID: target}}}}); err != nil {
		t.Fatal(err)
	}
	// Pion surfaces a repair only on the next read of the primary stream.
	timeout := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	next := total
	for repaired := false; !repaired; {
		select {
		case <-tick.C:
			packet := continuityPacket(900, uint16(1000+next), uint32(next)*1500, uint16(next), true)
			packet.Version, packet.PayloadType, packet.Marker = 2, 96, true
			f.router.ForwardVideo("pub", SlotCam, "", packet)
			next++
		case got := <-arrivals:
			repaired = got.seq == target
		case <-timeout:
			for _, publication := range f.router.VideoOperatorDiagnostics().Publications {
				for _, viewer := range publication.Viewers {
					encoded, _ := json.Marshal(viewer)
					t.Logf("diagnostics: %s", encoded)
				}
			}
			t.Fatalf("NACK for a packet %d positions old was not answered from the egress history", total-1)
		}
	}
	plis, nacks := f.upstream.counts()
	if nacks != 0 || plis != plisBefore {
		t.Fatalf("viewer repair reached the publisher: nacks=%d plis=%d->%d", nacks, plisBefore, plis)
	}
}
