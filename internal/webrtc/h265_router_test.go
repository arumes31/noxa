package webrtc

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

const hevcRouterFmtp = "profile-id=1;tier-flag=0;level-id=93"

func hevcRouterCodec(fmtp string) pion.RTPCodecCapability {
	return pion.RTPCodecCapability{MimeType: pion.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: fmtp}
}

type hevcRouterTrack struct {
	fakeVideoTrack
	packets []*rtp.Packet
	codecs  []pion.RTPCodecCapability
	index   int
	current pion.RTPCodecCapability
	before  func(int)
}

func (t *hevcRouterTrack) Codec() pion.RTPCodecParameters {
	return pion.RTPCodecParameters{RTPCodecCapability: t.current, PayloadType: 116}
}
func (t *hevcRouterTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	if t.index == len(t.packets) {
		return nil, nil, io.EOF
	}
	if t.before != nil {
		t.before(t.index)
	}
	p := t.packets[t.index]
	if len(t.codecs) > 0 {
		t.current = t.codecs[t.index]
	}
	t.index++
	return p, nil, nil
}
func hevcRouterPublication(t *testing.T, r *Router) uint64 {
	t.Helper()
	generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"h265", "h264", "vp8"}, 93)
	if err != nil {
		t.Fatal(err)
	}
	return generation
}
func hevcRouterTap(t *testing.T) (*Router, *fakeTrackWriter, *fakeRTCPWriter) {
	t.Helper()
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "recorder")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	hevcRouterPublication(t, r)
	out := &fakeTrackWriter{}
	r.addVideoOutput("recorder", out)
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	return r, out, feedback
}

func TestH265RouterFormatChangeCannotReusePartialUnit(t *testing.T) {
	r, out, _ := hevcRouterTap(t)
	first := hevcRouterCodec(hevcRouterFmtp)
	second := hevcRouterCodec(hevcRouterFmtp + ";tx-mode=SRST")
	track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: first,
		packets: []*rtp.Packet{
			hevcPacket(1, 90, false, hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false))...),
			hevcPacket(2, 90, false, 98, 1, 0x93, 0x80), hevcPacket(3, 90, true, 98, 1, 0x53, 1), hevcPacket(4, 180, true, hevcKey(19)...)},
		codecs: []pion.RTPCodecCapability{first, second, second, second}}
	r.ReadVideoLoop("pub", SlotScreen, track)
	if out.count() != 1 || out.packets[0].SequenceNumber != 4 {
		t.Fatalf("partial old-format unit escaped new codec epoch: %v", out.packets)
	}
}

func TestH265RouterRejectedProfileRecovery(t *testing.T) {
	for _, replacement := range []string{"delta", "configured IRAP"} {
		t.Run(replacement, func(t *testing.T) {
			r, out, feedback := hevcRouterTap(t)
			good := hevcRouterCodec(hevcRouterFmtp)
			bad := hevcRouterCodec("profile-id=2;tier-flag=0;level-id=93")
			track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: good,
				packets: []*rtp.Packet{hevcPacket(1, 90, true, hevcKey(19)...), hevcPacket(2, 180, false, hevcNAL(1, true)...), hevcPacket(3, 180, true, hevcNAL(1, false)...), recoveryPaddingPacket(7, 4, 0)},
				codecs:  []pion.RTPCodecCapability{good, bad, good, good}}
			wantPLI := 1
			if replacement == "delta" {
				track.packets = append(track.packets, hevcPacket(5, 270, true, hevcNAL(1, true)...))
				track.codecs = append(track.codecs, good)
			} else {
				wantPLI = 0
				track.packets = append(track.packets, hevcPacket(5, 270, false, hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false))...), hevcPacket(6, 270, false, 98, 1, 0x93, 0x80), hevcPacket(7, 270, true, 98, 1, 0x53, 1))
				track.codecs = append(track.codecs, good, good, good)
			}
			r.ReadVideoLoop("pub", SlotScreen, track)
			waitAsyncKeyframeRequests(t, r)
			if feedback.pliCount() != wantPLI {
				t.Fatalf("PLI=%d want=%d", feedback.pliCount(), wantPLI)
			}
			for _, p := range out.packets {
				if p.Timestamp == 180 {
					t.Fatal("rejected profile timestamp escaped")
				}
			}
			if out.count() < 3 {
				t.Fatal("valid replacement lost")
			}
		})
	}
}

func TestH265RouterMIMEChangeAbandonsPartialUnit(t *testing.T) {
	r, out, _ := hevcRouterTap(t)
	hevc := hevcRouterCodec(hevcRouterFmtp)
	vp8 := pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}
	middle := boundsKeyPacket(2, 180, 640, 360)
	middle.SSRC = 7
	track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: hevc, packets: []*rtp.Packet{hevcPacket(1, 90, false, 98, 1, 0x93, 0x80), middle, hevcPacket(3, 90, true, 98, 1, 0x53, 1), hevcPacket(4, 270, true, hevcKey(19)...)}, codecs: []pion.RTPCodecCapability{hevc, vp8, hevc, hevc}}
	r.ReadVideoLoop("pub", SlotScreen, track)
	if out.count() != 2 || out.packets[0].SequenceNumber != 2 || out.packets[1].SequenceNumber != 4 {
		t.Fatalf("codec-crossing partial frame forwarded: %v", out.packets)
	}
}

type hevcRouterCapture struct {
	pion.TrackLocalWriter
	packets []*rtp.Packet
	err     error
}

func (w *hevcRouterCapture) WriteRTP(h *rtp.Header, p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.packets = append(w.packets, &rtp.Packet{Header: h.Clone(), Payload: bytes.Clone(p)})
	return len(p), nil
}

type hevcRouterTrackContext struct {
	extensionTrackContext
	capture *hevcRouterCapture
}

func (c hevcRouterTrackContext) WriteStream() pion.TrackLocalWriter { return c.capture }

func TestH265RouterLateWatchCRAAndRASLNoFalsePLI(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	for _, id := range []string{"early", "late"} {
		attachFakePeer(t, e, r, id)
		r.JoinChannel(1, id)
	}
	r.JoinChannel(1, "pub")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	generation := hevcRouterPublication(t, r)
	captures := map[string]*hevcRouterCapture{"early": {}, "late": {}}
	bind := func(id string) {
		output := pubTrackFor(r, id, "pub").video[SlotScreen]
		if output.track.Codec().MimeType != pion.MimeTypeH265 {
			return
		}
		_, err := output.track.Bind(hevcRouterTrackContext{extensionTrackContext: extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 116}}, capture: captures[id]})
		if err != nil {
			t.Fatal(err)
		}
	}
	r.SetRenegotiateHook(bind)
	watch := func(id string) {
		if _, err := r.WatchVideoWithFormats(id, "pub", SlotScreen, generation, 1, r.VideoWatchSession(id), true, []string{"h265", "h264", "vp8"}, 93); err != nil {
			t.Fatal(err)
		}
	}
	watch("early")
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: hevcRouterCodec(hevcRouterFmtp), packets: []*rtp.Packet{
		hevcPacket(1, 90, true, hevcKey(19)...), hevcPacket(2, 180, true, hevcKey(21)...), hevcPacket(3, 120, true, hevcNAL(6, true)...),
		hevcPacket(4, 100, false, 98, 1, 0x88, 0x80), hevcPacket(5, 100, true, 98, 1, 0x48, 1), recoveryPaddingPacket(7, 6, 0), hevcPacket(7, 270, true, hevcNAL(1, true)...)}}
	track.before = func(index int) {
		waitVideoDispatchIdle(t, r)
		if index == 1 {
			watch("late")
			feedback.pkts = nil
		}
		r.mu.Lock()
		r.keyframeLast = make(map[uint32]time.Time)
		r.mu.Unlock()
	}
	r.ReadVideoLoop("pub", SlotScreen, track)
	early, late := captures["early"].packets, captures["late"].packets
	if len(early) != 7 || len(late) != 4 {
		t.Fatalf("early=%d late=%d; CRA/RADL/RASL forwarding incorrect", len(early), len(late))
	}
	for i, p := range late {
		if p.SequenceNumber != uint16(i+2) || p.Timestamp == 100 {
			t.Fatalf("late viewer got RASL or sequence gap: %+v", p.Header)
		}
	}
	if feedback.pliCount() != 0 {
		t.Fatalf("intentional RASL drop requested %d PLIs", feedback.pliCount())
	}
}

func TestH265RouterFailedWriteAndResumeWaitForConfiguredIRAP(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(1, "pub")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	generation := hevcRouterPublication(t, r)
	capture := &hevcRouterCapture{err: errors.New("fixture rejects first complete IRAP")}
	r.SetRenegotiateHook(func(id string) {
		output := pubTrackFor(r, id, "pub").video[SlotScreen]
		if output.track.Codec().MimeType != pion.MimeTypeH265 {
			return
		}
		_, err := output.track.Bind(hevcRouterTrackContext{extensionTrackContext: extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 116}}, capture: capture})
		if err != nil {
			t.Fatal(err)
		}
	})
	watch := func(revision uint64, active bool) {
		if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, revision, r.VideoWatchSession("sub"), active, []string{"h265", "h264", "vp8"}, 93); err != nil {
			t.Fatal(err)
		}
	}
	watch(1, true)
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	track := &hevcRouterTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 7}, current: hevcRouterCodec(hevcRouterFmtp), packets: []*rtp.Packet{
		hevcPacket(1, 90, true, hevcKey(21)...), hevcPacket(2, 180, true, hevcNAL(1, true)...), hevcPacket(3, 270, true, hevcKey(21)...), hevcPacket(4, 240, true, hevcNAL(8, true)...), hevcPacket(5, 360, true, hevcNAL(1, true)...),
		hevcPacket(6, 450, true, hevcNAL(1, true)...), hevcPacket(7, 540, true, hevcKey(21)...), hevcPacket(8, 500, true, hevcNAL(8, true)...), hevcPacket(9, 630, true, hevcNAL(1, true)...)}}
	track.before = func(i int) {
		waitVideoDispatchIdle(t, r)
		if i == 1 {
			capture.err = nil
			out := pubTrackFor(r, "sub", "pub").video[SlotScreen]
			if out.videoContinuity.seen || out.videoContinuity.h265DropRASL {
				t.Fatal("failed first write committed HEVC continuity")
			}
		}
		if i == 5 {
			watch(2, false)
			watch(3, true)
		}
		r.mu.Lock()
		r.keyframeLast = make(map[uint32]time.Time)
		r.mu.Unlock()
	}
	r.ReadVideoLoop("pub", SlotScreen, track)
	if len(capture.packets) != 4 {
		t.Fatalf("write failure/resume forwarded incomplete epoch: %d packets", len(capture.packets))
	}
	for i, want := range []uint32{270, 360, 540, 630} {
		if capture.packets[i].Timestamp != want {
			t.Fatalf("frame %d timestamp %d want %d", i, capture.packets[i].Timestamp, want)
		}
	}
	if feedback.pliCount() < 2 {
		t.Fatal("failed startup/resume deltas did not request keyframe repair")
	}
}
func TestH265RouterWatchRevisionDuringRASLDoesNotCommitStartup(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(1, "pub")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	generation := hevcRouterPublication(t, r)
	capture := &hevcRouterCapture{}
	r.SetRenegotiateHook(func(id string) {
		output := pubTrackFor(r, id, "pub").video[SlotScreen]
		if output.track.Codec().MimeType != pion.MimeTypeH265 {
			return
		}
		_, err := output.track.Bind(hevcRouterTrackContext{extensionTrackContext: extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 116}}, capture: capture})
		if err != nil {
			t.Fatal(err)
		}
	})
	watch := func(revision uint64) {
		if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, revision, r.VideoWatchSession("sub"), true, []string{"h265", "h264", "vp8"}, 93); err != nil {
			t.Fatal(err)
		}
	}
	watch(1)
	token, ok := r.claimSlot("pub", SlotScreen+slotSep)
	if !ok {
		t.Fatal("claim")
	}
	defer r.releaseSlot("pub", SlotScreen+slotSep, token)
	registerVideoSource(r, "pub", SlotScreen, "", 7)
	if !r.setVideoCodec("pub", SlotScreen, "", token, hevcRouterCodec(hevcRouterFmtp)) {
		t.Fatal("codec")
	}
	now := time.Now()
	policy := r.videoPolicySnapshot()
	send := func(unit []*rtp.Packet, index int, key bool) int {
		return r.forwardVideoFrameAtPolicyCodec("pub", SlotScreen, "", unit[index], now, policy, true, pion.MimeTypeH265, key && index == 0, unit)
	}
	initial := []*rtp.Packet{hevcPacket(1, 90, true, hevcKey(21)...)}
	if send(initial, 0, true) != 1 {
		t.Fatal("initial CRA not delivered")
	}
	output := pubTrackFor(r, "sub", "pub").video[SlotScreen]
	oldStartup := output.videoStartupEpoch.Load()
	if oldStartup == 0 {
		t.Fatal("initial watch did not start")
	}
	// This is an already assembled complete RASL unit. Revise the active
	// watch between its fanout calls, exactly where another control goroutine
	// can run while ReadVideoLoop forwards an assembled multi-packet unit.
	rasl := []*rtp.Packet{hevcPacket(2, 60, false, 98, 1, 0x88, 0x80), hevcPacket(3, 60, true, 98, 1, 0x48, 1)}
	if send(rasl, 0, false) != 0 || !output.videoContinuity.h265DropUnit {
		t.Fatal("RASL prefix not intentionally discarded")
	}
	watch(2)
	if pubTrackFor(r, "sub", "pub").video[SlotScreen] != output {
		t.Fatal("active revision unexpectedly replaced output; fixture does not cover retained state")
	}
	r.watchMu.RLock()
	newEpoch := r.watches[watchKey{"sub", "pub", SlotScreen}].epoch
	r.watchMu.RUnlock()
	if newEpoch == oldStartup {
		t.Fatal("watch revision did not create an epoch")
	}
	if send(rasl, 1, false) != 0 {
		t.Fatal("RASL suffix forwarded after revision")
	}
	if output.videoStartupEpoch.Load() != oldStartup {
		t.Fatal("discarded RASL suffix incorrectly marked new watch started")
	}
	delta := []*rtp.Packet{hevcPacket(4, 180, true, hevcNAL(1, true)...)}
	if send(delta, 0, false) != 0 || output.videoStartupEpoch.Load() != oldStartup {
		t.Fatal("delta started revised watch")
	}
	repair := []*rtp.Packet{hevcPacket(5, 270, true, hevcKey(19)...)}
	if send(repair, 0, true) != 1 || output.videoStartupEpoch.Load() != newEpoch {
		t.Fatal("configured replacement IRAP did not start revised watch")
	}
	if len(capture.packets) != 2 {
		t.Fatal("undecodable pictures escaped revised watch")
	}
}
func TestH265RouterSkippedUnitPrefixStillClassifiesRASL(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sub")
	r.JoinChannel(1, "sub")
	r.JoinChannel(1, "pub")
	r.SetTrackSlots("pub", map[string]string{"desktop": SlotScreen})
	generation := hevcRouterPublication(t, r)
	capture := &hevcRouterCapture{}
	r.SetRenegotiateHook(func(id string) {
		output := pubTrackFor(r, id, "pub").video[SlotScreen]
		if output.track.Codec().MimeType != pion.MimeTypeH265 {
			return
		}
		_, err := output.track.Bind(hevcRouterTrackContext{extensionTrackContext: extensionTrackContext{codec: pion.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 116}}, capture: capture})
		if err != nil {
			t.Fatal(err)
		}
	})
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true, []string{"h265", "h264", "vp8"}, 93); err != nil {
		t.Fatal(err)
	}
	token, ok := r.claimSlot("pub", SlotScreen+slotSep)
	if !ok {
		t.Fatal("claim")
	}
	defer r.releaseSlot("pub", SlotScreen+slotSep, token)
	registerVideoSource(r, "pub", SlotScreen, "", 7)
	if !r.setVideoCodec("pub", SlotScreen, "", token, hevcRouterCodec(hevcRouterFmtp)) {
		t.Fatal("codec")
	}
	const bitrate = 100000
	if err := r.SetVideoBitrateLimit(bitrate); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	policy := r.videoPolicySnapshot()
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	send := func(unit []*rtp.Packet, index int, key bool) int {
		return r.forwardVideoFrameAtPolicyCodec("pub", SlotScreen, "", unit[index], now, policy, true, pion.MimeTypeH265, key && index == 0, unit)
	}
	initial := []*rtp.Packet{hevcPacket(1, 90, true, hevcKey(21)...)}
	if send(initial, 0, true) != 1 {
		t.Fatal("initial CRA not delivered")
	}
	output := pubTrackFor(r, "sub", "pub").video[SlotScreen]
	// Exhaust the real byte bucket after the CRA, forcing fanout to skip the
	// first packet before it can classify the next complete unit for a viewer.
	if !r.allowVideoPacket("pub", bitrate/8-initial[0].MarshalSize(), now) {
		t.Fatal("fixture did not exhaust available byte budget")
	}
	rasl := []*rtp.Packet{hevcPacket(2, 60, false, 98, 1, 0x88, 0x80), hevcPacket(3, 60, true, 98, 1, 0x48, 1)}
	if send(rasl, 0, false) != 0 || output.videoContinuity.h265DropUnit {
		t.Fatal("first RASL packet was not skipped before viewer classification")
	}
	now = now.Add(time.Second)
	if send(rasl, 1, false) != 0 {
		t.Fatal("unclassified RASL suffix escaped after its first packet was skipped")
	}
	trailing := []*rtp.Packet{hevcPacket(4, 180, true, hevcNAL(1, true)...)}
	if send(trailing, 0, false) != 1 {
		t.Fatal("next complete trailing unit was not delivered")
	}
	if len(capture.packets) != 2 || capture.packets[1].SequenceNumber != 2 || capture.packets[1].Timestamp != 180 {
		t.Fatal("skipped RASL unit leaked or created downstream sequence gap")
	}
	if feedback.pliCount() != 0 {
		t.Fatal("intentional RASL discard requested a keyframe")
	}
}
