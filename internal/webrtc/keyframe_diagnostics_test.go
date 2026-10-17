package webrtc

import (
	"errors"
	"testing"
	"time"

	"noxa/internal/netproto"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type keyframeDiagnosticWriter func([]rtcp.Packet) error

func (f keyframeDiagnosticWriter) WriteRTCP(p []rtcp.Packet) error { return f(p) }

func keyframeDiagnosticSource(r *Router, rid string, ssrc uint32) *videoIngressDiagnostic {
	registerVideoSource(r, "pub", SlotScreen, rid, ssrc)
	d := &videoIngressDiagnostic{ssrc: ssrc, started: time.Now()}
	if r.videoIngress == nil {
		r.videoIngress = make(map[videoIngressKey]*videoIngressDiagnostic)
	}
	r.videoIngress[videoIngressKey{"pub", SlotScreen, rid}] = d
	return d
}

func TestKeyframeDiagnosticsCoalescingAndFailedWrite(t *testing.T) {
	r := NewRouter(nil)
	d := keyframeDiagnosticSource(r, "f", 42)
	writes := 0
	r.rtcpWriters["pub"] = keyframeDiagnosticWriter(func(p []rtcp.Packet) error {
		writes++
		if len(p) != 1 || p[0].(*rtcp.PictureLossIndication).MediaSSRC != 42 {
			t.Fatal("changed RTCP request")
		}
		return nil
	})
	r.requestKeyframe("pub", SlotScreen, "f", keyframeH264Assembly)
	r.requestKeyframe("pub", SlotScreen, "f", keyframeViewerFeedback)
	got := d.keyframeRequestSnapshot()
	if writes != 1 || got.H264Assembly != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) || got.ViewerFeedback != (netproto.VideoKeyframeRequestCounters{Requests: 1, Coalesced: 1}) {
		t.Fatalf("request/write/coalescing conflated: %+v", got)
	}
	r.rtcpWriters["pub"] = keyframeDiagnosticWriter(func([]rtcp.Packet) error { writes++; return errors.New("test failure") })
	clear(r.keyframeLast)
	r.RequestKeyframe("pub", SlotScreen, "f")
	failed := d.keyframeRequestSnapshot()
	if writes != 2 || failed.Explicit != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteFailed: 1}) || got.Explicit.Requests != 0 {
		t.Fatal("failed write counted as success or snapshot aliased")
	}
	// A write failure still consumes the original coalescing window.
	r.RequestKeyframe("pub", SlotScreen, "f")
	if writes != 2 || d.keyframeRequestSnapshot().Explicit.Coalesced != 1 {
		t.Fatal("diagnostics altered failed-write throttle")
	}
}

func TestKeyframeDiagnosticsFallbackAndMissingTargets(t *testing.T) {
	r := NewRouter(nil)
	d := keyframeDiagnosticSource(r, "h", 42)
	r.RequestKeyframe("pub", SlotScreen, "missing")
	if d.keyframeRequestSnapshot() != nil {
		t.Fatal("missing writer claimed resolved request")
	}
	writer := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = writer
	r.RequestKeyframe("missing", SlotScreen, "h")
	r.RequestKeyframe("pub", SlotCam, "h")
	if writer.pliCount() != 0 || d.keyframeRequestSnapshot() != nil {
		t.Fatal("unknown target counted")
	}
	r.RequestKeyframe("pub", SlotScreen, "missing")
	if writer.pliCount() != 1 || d.keyframeRequestSnapshot().Explicit.WriteSucceeded != 1 {
		t.Fatal("fallback not charged to actual RID")
	}
	clear(r.keyframeLast)
	// Stale diagnostic registration must never acquire a different SSRC's counters.
	d.ssrc = 99
	before := *d.keyframeRequestSnapshot()
	r.RequestKeyframe("pub", SlotScreen, "h")
	if writer.pliCount() != 2 || *d.keyframeRequestSnapshot() != before {
		t.Fatal("stale diagnostic altered routing or counters")
	}
}

func TestKeyframeDiagnosticsLateWriteCannotChargeReplacement(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "new_pointer"
		if reset {
			name = "same_pointer_new_epoch"
		}
		t.Run(name, func(t *testing.T) {
			r := NewRouter(nil)
			old := keyframeDiagnosticSource(r, "f", 42)
			next := old
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			r.rtcpWriters["pub"] = keyframeDiagnosticWriter(func([]rtcp.Packet) error { close(entered); <-release; return nil })
			go func() { defer close(done); r.RequestKeyframe("pub", SlotScreen, "f") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("write did not start")
			}
			if reset {
				old.mu.Lock()
				old.started = old.started.Add(time.Second)
				old.keyframeRequests = nil
				old.mu.Unlock()
			} else {
				r.mu.Lock()
				next = &videoIngressDiagnostic{ssrc: 42, started: old.started.Add(time.Second)}
				r.videoIngress[videoIngressKey{"pub", SlotScreen, "f"}] = next
				r.mu.Unlock()
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("write did not finish")
			}
			if next.keyframeRequestSnapshot() != nil {
				t.Fatal("late old-epoch write charged replacement")
			}
			if !reset && old.keyframeRequestSnapshot().Explicit.WriteSucceeded != 1 {
				t.Fatal("detached old epoch lost own outcome")
			}
		})
	}
}

func TestKeyframeDiagnosticsSemanticCallers(t *testing.T) {
	for _, kind := range []string{"watch", "stream_quality", "legacy_quality", "join", "ensure", "recording", "viewer"} {
		t.Run(kind, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "sub")
			generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			session := r.VideoWatchSession("sub")
			if kind != "watch" {
				if _, err = r.WatchVideo("sub", "pub", SlotScreen, generation, 1, session, true); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "join" || kind == "ensure" {
				engine, createErr := New(testLogger(), nil, false)
				if createErr != nil {
					t.Fatal(createErr)
				}
				t.Cleanup(func() {
					if err := engine.Close(); err != nil {
						t.Error(err)
					}
				})
				attachFakePeer(t, engine, r, "sub") // explicit direct-output recipient
			}

			d := keyframeDiagnosticSource(r, "f", 42)
			writer := &fakeRTCPWriter{}
			r.rtcpWriters["pub"] = writer
			switch kind {
			case "watch":
				r.pubPeers["sub"] = &PeerConnectionWrapper{} // direct output fixture
				_, err = r.WatchVideo("sub", "pub", SlotScreen, generation, 1, session, true)
			case "stream_quality":
				err = r.SetStreamVideoQuality("sub", "pub", SlotScreen, generation, session, "high")
			case "legacy_quality":
				err = r.SetVideoQuality("sub", "high")
			case "join":
				r.JoinChannel(1, "sub")
			case "ensure":
				r.EnsurePublishers("sub")
			case "recording":
				(&Voice{router: r}).RequestSourceKeyframe("pub", SlotScreen, 42)
			case "viewer":
				sender := &webrtc.RTPSender{}
				r.pubTracks["sub"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotScreen: {sender: sender}}}}
				r.relayKeyframeRequest("sub", sender)
				delete(r.pubTracks["sub"], "pub")
				clear(r.keyframeLast)
				r.relayKeyframeRequest("sub", sender)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := d.keyframeRequestSnapshot()
			if writer.pliCount() != 1 || got == nil {
				t.Fatalf("caller %s emitted %d writes", kind, writer.pliCount())
			}
			var selected netproto.VideoKeyframeRequestCounters
			switch kind {
			case "watch":
				selected = got.WatchStart
			case "stream_quality", "legacy_quality":
				selected = got.QualityChange
			case "join", "ensure":
				selected = got.ConsumerStart
			case "recording":
				selected = got.RecordingSource
			case "viewer":
				selected = got.ViewerFeedback
			}
			if selected != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) {
				t.Fatalf("caller reason mismatch: %+v", got)
			}
		})
	}
}

func TestKeyframeDiagnosticsRejectsResetBeforeFirstRecord(t *testing.T) {
	d := &videoIngressDiagnostic{started: time.Unix(1, 0)}
	selected := d.started
	d.started = d.started.Add(time.Second)
	d.recordKeyframeRequest(keyframeExplicit, keyframeRequested, &selected)
	d.recordKeyframeRequest(keyframeExplicit, keyframeWriteSucceeded, &selected)
	if d.keyframeRequestSnapshot() != nil {
		t.Fatal("request selected before reset charged new epoch")
	}
}

type keyframeDiagnosticH264Track struct{ diagnosticVideoTrack }

func (*keyframeDiagnosticH264Track) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42001f"}, PayloadType: 102}
}

func TestKeyframeDiagnosticsH264ReadLoopReason(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	track := &keyframeDiagnosticH264Track{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 42}, packets: make(chan *rtp.Packet)}}
	wrote := make(chan struct{}, 1)
	r.rtcpWriters["pub"] = keyframeDiagnosticWriter(func([]rtcp.Packet) error { wrote <- struct{}{}; return nil })
	done := make(chan struct{})
	go func() { defer close(done); r.ReadVideoLoop("pub", SlotScreen, track) }()
	t.Cleanup(func() { close(track.packets); <-done })
	// Unsupported STAP-B is rejected by the real H264 assembler.
	select {
	case track.packets <- h264ForwardPacket(1, 90, true, 25, 1):
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("assembler did not request keyframe")
	}
	// A second read establishes that the previous write and its counters finished.
	select {
	case track.packets <- h264ForwardPacket(2, 180, true, 0x41, 1):
	case <-time.After(time.Second):
		t.Fatal("reader stopped")
	}
	r.mu.RLock()
	d := r.videoIngress[videoIngressKey{"pub", SlotScreen, "f"}]
	r.mu.RUnlock()
	// The read loop hands units to the ingress dispatcher goroutine, so the
	// second read does not prove the first request's write was recorded.
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		got := d.keyframeRequestSnapshot()
		rejects := d.h264RejectSnapshot()
		d.mu.Unlock()
		if got != nil && got.H264Assembly == (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) && rejects != nil && rejects.Malformed == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("assembler provenance lost: %+v, %+v", got, rejects)
		}
		time.Sleep(time.Millisecond)
	}
}

type keyframeDiagnosticVP8Track struct{ diagnosticVideoTrack }

func (*keyframeDiagnosticVP8Track) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}}
}

func TestKeyframeDiagnosticsVP8BoundsReadLoopReason(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "pub")
	track := &keyframeDiagnosticVP8Track{diagnosticVideoTrack: diagnosticVideoTrack{fakeVideoTrack: fakeVideoTrack{rid: "f", ssrc: 42}, packets: make(chan *rtp.Packet)}}
	wrote := make(chan struct{}, 1)
	r.rtcpWriters["pub"] = keyframeDiagnosticWriter(func([]rtcp.Packet) error { wrote <- struct{}{}; return nil })
	done := make(chan struct{})
	go func() { defer close(done); r.ReadVideoLoop("pub", SlotScreen, track) }()
	t.Cleanup(func() { close(track.packets); <-done })
	select {
	case track.packets <- boundsKeyPacket(1, 90, 1920, 1080):
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("bounds did not request keyframe")
	}
	select {
	case track.packets <- boundsKeyPacket(2, 180, 640, 360):
	case <-time.After(time.Second):
		t.Fatal("reader stopped")
	}
	r.mu.RLock()
	d := r.videoIngress[videoIngressKey{"pub", SlotScreen, "f"}]
	r.mu.RUnlock()
	d.mu.Lock()
	got := d.keyframeRequestSnapshot()
	d.mu.Unlock()
	if got == nil || got.VP8Bounds != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) {
		t.Fatalf("bounds provenance lost: %+v", got)
	}
}

func TestKeyframeDiagnosticsAwaitKeyframeReason(t *testing.T) {
	engine, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, engine, r, "viewer")
	r.JoinChannel(1, "viewer")
	r.JoinChannel(1, "pub")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	testVideoPublication(t, r, "pub", "viewer", SlotScreen)
	d := keyframeDiagnosticSource(r, "f", 42)
	writer := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = writer
	output := pubTrackFor(r, "viewer", "pub").video[SlotScreen]
	if _, err = output.track.Bind(extensionTrackContext{codec: webrtc.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 96}, writer: &extensionCapture{}}); err != nil {
		t.Fatal(err)
	}
	if r.ForwardVideo("pub", SlotScreen, "f", continuityPacket(42, 100, 90000, 10, false)) != 0 {
		t.Fatal("unseen delta was forwarded")
	}
	waitAsyncKeyframeRequests(t, r)
	got := d.keyframeRequestSnapshot()
	if writer.pliCount() != 1 || got == nil || got.AwaitKeyframe != (netproto.VideoKeyframeRequestCounters{Requests: 1, WriteSucceeded: 1}) {
		t.Fatalf("waiting output provenance lost: %+v", got)
	}
}
