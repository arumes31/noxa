package webrtc

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestVideoStageDiagnosticsCountsMediaFramesAndExpiresRates(t *testing.T) {
	var stage videoStageCounter
	now := time.Unix(100, 0)
	if got := stage.snapshot(now); got.FPS != nil || !got.Stale {
		t.Fatal("empty stage claimed a measured rate")
	}
	for index := range 60 {
		at := now.Add(time.Duration(index) * time.Second / 60)
		header := &rtp.Header{Timestamp: uint32(index * 1500), Marker: true}
		stage.observe(header, 1000, true, at)
		stage.observe(header, 1000, true, at) // Retransmission of the same frame.
	}
	stage.observe(&rtp.Header{Timestamp: 90_000}, 200, false, now.Add(time.Second)) // Padding.
	stage.observe(&rtp.Header{Timestamp: 0}, 1000, true, now.Add(time.Second))      // Late repair.
	got := stage.snapshot(now.Add(time.Second))
	if got.Frames != 60 || got.Packets != 122 || got.Bytes != 121200 || got.FPS == nil || *got.FPS != 60 || got.BitrateBPS == nil || *got.BitrateBPS != 969600 {
		t.Fatalf("frame/packet/bitrate measurement mismatch: %+v", got)
	}
	got = stage.snapshot(now.Add(4 * time.Second))
	if !got.Stale || got.FPS == nil || *got.FPS != 0 || got.BitrateBPS == nil || *got.BitrateBPS != 0 {
		t.Fatalf("idle stage retained a live rate: %+v", got)
	}
	for index := range 10000 {
		stage.observe(&rtp.Header{Timestamp: uint32(index)}, 1, true, now.Add(5*time.Second))
	}
	if len(stage.timestamps) > 512 {
		t.Fatal("timestamp de-duplication storage is unbounded")
	}
}

func TestVideoForwardDiagnosticsRequiresSuccessfulFinalWrite(t *testing.T) {
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	stream.outputSSRC.Store(42)
	allowed := false
	source := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, Timestamp: 9000, Marker: true}, Payload: []byte{0x10, 1}}
	packet, ok := stream.prepare(source, mediaTicket{videoSource: 99, videoRID: "f", delivery: MediaDelivery{Slot: SlotScreen, Publication: 5, WatchEpoch: 6},
		guard: func(_ MediaDelivery, write func() error) error {
			if allowed {
				return write()
			}
			return nil
		}})
	if !ok {
		t.Fatal("packet admission failed")
	}
	writer := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		return h.MarshalSize() + len(payload), nil
	})
	if !stream.videoDiagnostic.stage.last.IsZero() {
		t.Fatal("admission counted as forwarding")
	}
	_, _ = stream.write(&packet.Header, packet.Payload, nil, writer)
	if !stream.videoDiagnostic.stage.last.IsZero() {
		t.Fatal("denied packet counted as forwarding")
	}
	allowed = true
	for range 2 {
		_, _ = stream.write(&packet.Header, packet.Payload, nil, writer)
	}
	if got := stream.videoDiagnostic.stage; got.packets != 2 || got.frames != 1 {
		t.Fatalf("forwarding/repair counts = %+v", got)
	}
	failure := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 0, errors.New("socket failed") })
	_, _ = stream.write(&packet.Header, packet.Payload, nil, failure)
	if stream.videoDiagnostic.stage.packets != 2 {
		t.Fatal("failed final write counted as forwarding")
	}
}

func TestVideoForwardDiagnosticsIgnoresRetiredLayerRepairOnPrimarySSRC(t *testing.T) {
	stream := &mediaEgressStream{}
	stream.outputSSRC.Store(42)
	now := time.Unix(1, 0)
	old := mediaTicket{id: 1, videoSource: 99, videoRID: "h", videoMedia: true, delivery: MediaDelivery{Slot: SlotScreen, Publication: 3, WatchEpoch: 4}}
	current := old
	current.id, current.videoSource, current.videoRID = 2, 100, "f"
	header := &rtp.Header{SSRC: 42, Timestamp: 1500}
	stream.observeVideo(header, 1200, &old, now)
	header.Timestamp = 3000
	stream.observeVideo(header, 1200, &current, now.Add(time.Millisecond))
	header.Timestamp = 1500 // RTX without a separate negotiated retransmission SSRC.
	stream.observeVideo(header, 1200, &old, now.Add(2*time.Millisecond))
	if got := &stream.videoDiagnostic; got.sourceSSRC != 100 || got.rid != "f" || got.stage.frames != 1 || got.stage.packets != 1 {
		t.Fatalf("retired retransmission replaced current attribution: source=%d RID=%s frames=%d packets=%d", got.sourceSSRC, got.rid, got.stage.frames, got.stage.packets)
	}
	stream.observeVideo(&rtp.Header{SSRC: 42, Timestamp: 3000}, 1200, &current, now.Add(3*time.Millisecond))
	if stream.videoDiagnostic.stage.packets != 2 || stream.videoDiagnostic.stage.frames != 1 {
		t.Fatal("current layer retransmission must count bytes/packets without another frame")
	}
}

func TestStreamDiagnosticsEligibilityAndPublicationScope(t *testing.T) {
	for _, test := range []string{"eligible before watching", "stale session", "stale generation", "wrong channel", "denied publisher", "stopped publication", "rebuild"} {
		t.Run(test, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "publisher")
			r.JoinChannel(1, "member")
			generation, err := r.PublishVideo("publisher", SlotScreen, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			session := r.VideoWatchSession("member")
			switch test {
			case "stale session":
				session++
			case "stale generation":
				generation++
			case "wrong channel":
				r.JoinChannel(2, "member")
			case "denied publisher":
				r.SetPublisherGuard(func(PublisherAccess) bool { return false })
			case "stopped publication":
				_, _ = r.PublishVideo("publisher", SlotScreen, generation, false)
			case "rebuild":
				r.DetachPeerKeepChannel("member")
			}
			got, err := r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
			if test == "eligible before watching" {
				if err != nil || got.Generation != generation || got.Session != session || got.Forwarding != nil {
					t.Fatalf("eligible member result = %+v, %v", got, err)
				}
			} else if !errors.Is(err, ErrVideoWatch) || got != nil {
				t.Fatalf("stale/unauthorized result = %+v, %v", got, err)
			}
		})
	}
}

func TestStreamDiagnosticsForwardingFollowsWatchEpochAndPublication(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "member")
	generation := testVideoPublication(t, r, "publisher", "member", SlotScreen)
	session := r.VideoWatchSession("member")
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	stream.outputSSRC.Store(42)
	r.pubTracks["member"] = map[string]*pubTrack{"publisher": {video: map[string]*pubSlot{SlotScreen: {egress: stream}}}}
	watch := r.watches[watchKey{"member", "publisher", SlotScreen}]
	ticket := &mediaTicket{videoSource: 99, videoRID: "f", videoMedia: true, delivery: MediaDelivery{Slot: SlotScreen, Publication: generation, WatchEpoch: watch.epoch}}
	stream.observeVideo(&rtp.Header{SSRC: 42, Timestamp: 1500}, 1200, ticket, time.Now())
	got, err := r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
	if err != nil || got.Forwarding == nil || got.Forwarding.SourceSSRC != 99 || got.Forwarding.OutputSSRC != 42 || got.Forwarding.Stage.Frames != 1 {
		t.Fatalf("active forwarding binding missing: %+v, %v", got, err)
	}
	if _, err := r.WatchVideo("member", "publisher", SlotScreen, generation, 2, session, false); err != nil {
		t.Fatal(err)
	}
	got, err = r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
	if err != nil || got.Forwarding != nil {
		t.Fatal("stopped watch retained forwarding details")
	}
	if _, err := r.WatchVideo("member", "publisher", SlotScreen, generation, 3, session, true); err != nil {
		t.Fatal(err)
	}
	got, err = r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
	if err != nil || got.Forwarding != nil {
		t.Fatal("resumed watch exposed the prior watch's counters")
	}
	input := &videoIngressDiagnostic{ssrc: 99, started: time.Now()}
	input.observe(&rtp.Packet{Header: rtp.Header{Timestamp: 1500}, Payload: []byte{1}}, time.Now())
	r.videoIngress = map[videoIngressKey]*videoIngressDiagnostic{{"publisher", SlotScreen, "f"}: input}
	registerVideoSource(r, "publisher", SlotScreen, "f", 99)
	if _, err := r.PublishVideo("publisher", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	newGeneration, err := r.PublishVideo("publisher", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.StreamDiagnostics("member", "publisher", SlotScreen, newGeneration, session)
	if err != nil || len(got.Layers) != 1 || got.Layers[0].Ingress.Packets != 0 || got.Layers[0].Ingress.FPS != nil {
		t.Fatalf("reused track retained prior publication counters: %+v, %v", got, err)
	}
}

type diagnosticVideoTrack struct {
	fakeVideoTrack
	packets chan *rtp.Packet
}

func (t *diagnosticVideoTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	packet, ok := <-t.packets
	if !ok {
		return nil, nil, io.EOF
	}
	return packet, nil, nil
}

func TestVideoIngressDiagnosticsPrecedesRepairAndExpiresWithTrack(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "member")
	generation, _ := r.PublishVideo("publisher", SlotScreen, 0, true)
	session := r.VideoWatchSession("member")
	track := &diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 42}, packets: make(chan *rtp.Packet)}
	done := make(chan struct{})
	go func() { defer close(done); r.ReadVideoLoop("publisher", SlotScreen, track) }()
	var stop sync.Once
	t.Cleanup(func() { stop.Do(func() { close(track.packets) }); <-done })
	track.packets <- boundsKeyPacket(1, 1, 1920, 1080)
	track.packets <- boundsDeltaPacket(3, 3)
	// The second send returns as the loop starts observing it; synchronise a
	// third receive so both packets have reached the pre-repair counters.
	track.packets <- boundsDeltaPacket(4, 4)
	got, err := r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
	if err != nil || len(got.Layers) != 1 || got.Layers[0].Ingress.Packets < 2 {
		t.Fatalf("held packet absent from ingress diagnostics: %+v, %v", got, err)
	}
	stop.Do(func() { close(track.packets) })
	<-done
	got, err = r.StreamDiagnostics("member", "publisher", SlotScreen, generation, session)
	if err != nil || len(got.Layers) != 0 {
		t.Fatalf("ended track retained diagnostics: %+v, %v", got, err)
	}
}
