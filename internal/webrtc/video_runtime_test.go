package webrtc

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type updatingVideoTrack struct {
	boundsVideoTrack
	beforeRead func(int)
}

func (t *updatingVideoTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	if t.beforeRead != nil {
		t.beforeRead(t.reads())
	}
	return t.fakeTrackReader.ReadRTP()
}

func setRuntimeVideoLimits(t *testing.T, r *Router, bitrate int, bounds VideoBounds) {
	t.Helper()
	if err := r.SetVideoLimits(bitrate, bounds); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeVideoLimitsValidationAndNoopPreserveBudget(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	bounds := VideoBounds{640, 360}
	setRuntimeVideoLimits(t, r, 8000, bounds)
	at := time.Unix(100, 0)
	if !r.allowVideoPacket("publisher", 1000, at) {
		t.Fatal("initial budget unavailable")
	}
	before := r.videoPolicySnapshot()
	for _, invalid := range []struct {
		bitrate int
		bounds  VideoBounds
	}{
		{-1, VideoBounds{1280, 720}}, {100000001, bounds}, {16000, VideoBounds{640, 0}}, {0, VideoBounds{16384, 1}},
	} {
		if err := r.SetVideoLimits(invalid.bitrate, invalid.bounds); err == nil {
			t.Fatal("invalid compound limits accepted")
		}
		if r.videoPolicySnapshot() != before || r.videoBitrateLimit != 8000 || r.allowVideoPacket("publisher", 1, at) {
			t.Fatal("invalid update changed policy or refilled publisher budget")
		}
	}
	setRuntimeVideoLimits(t, r, 8000, bounds)
	if r.videoPolicySnapshot() != before || r.allowVideoPacket("publisher", 1, at) {
		t.Fatal("no-op reset policy or budget")
	}
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	setRuntimeVideoLimits(t, r, 0, bounds)
	if r.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(1, 1, 640, 360)) != 0 || tap.count() != 0 {
		t.Fatal("uninspected forwarding bypassed bounds")
	}
}

func TestRuntimeVideoBoundsRoundTripRequiresNewKeyframe(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{640, 360})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	track := &updatingVideoTrack{boundsVideoTrack: boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsDeltaPacket(2, 2), boundsKeyPacket(3, 3, 640, 360),
	}}}, mime: webrtc.MimeTypeVP8}}
	track.beforeRead = func(read int) {
		if read == 1 {
			setRuntimeVideoLimits(t, r, 0, VideoBounds{})
			setRuntimeVideoLimits(t, r, 0, VideoBounds{640, 360})
		}
	}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 2 || tap.packets[1].SequenceNumber != 3 {
		t.Fatal("bounds round trip reused previous reference frame")
	}
}

func TestRuntimeVideoBoundsChangeAfterInspectionDropsPacket(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	r.SetMediaGuard(func(_ MediaDelivery, write func() error) error {
		if err := r.SetVideoLimits(0, VideoBounds{640, 360}); err != nil {
			return err
		}
		return write()
	})
	track := &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 1280, 720), boundsDeltaPacket(2, 2), boundsKeyPacket(3, 3, 640, 360),
	}}}, mime: webrtc.MimeTypeVP8}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 1 || tap.packets[0].SequenceNumber != 3 {
		t.Fatal("stale inspection crossed dimension change")
	}
}

func TestRuntimeVideoBoundsRecheckExistingTrack(t *testing.T) {
	for _, slot := range []string{SlotCam, SlotScreen} {
		t.Run(slot, func(t *testing.T) {
			r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
			if err != nil {
				t.Fatal(err)
			}
			r.JoinChannel(1, "publisher")
			r.JoinChannel(1, "recorder")
			r.SetTrackSlots("publisher", map[string]string{"video": slot})
			tap := &fakeTrackWriter{}
			r.addVideoOutput("recorder", tap)
			track := &updatingVideoTrack{boundsVideoTrack: boundsVideoTrack{
				fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
					boundsKeyPacket(1, 1, 1280, 720), boundsDeltaPacket(2, 2),
					boundsKeyPacket(3, 3, 1280, 720), boundsKeyPacket(4, 4, 640, 360),
					boundsDeltaPacket(5, 5), boundsDeltaPacket(6, 6),
					boundsDeltaPacket(7, 7), boundsKeyPacket(8, 8, 640, 360),
				}}}, mime: webrtc.MimeTypeVP8,
			}}
			track.beforeRead = func(read int) {
				switch read {
				case 1:
					setRuntimeVideoLimits(t, r, 0, VideoBounds{640, 360})
				case 4:
					setRuntimeVideoLimits(t, r, 8000000, VideoBounds{640, 360})
					if err := r.SetVideoLimits(0, VideoBounds{640, 0}); err == nil {
						t.Fatal("invalid bounds accepted")
					}
					setRuntimeVideoLimits(t, r, 8000000, VideoBounds{640, 360})
				case 5:
					setRuntimeVideoLimits(t, r, 0, VideoBounds{})
				case 6:
					setRuntimeVideoLimits(t, r, 0, VideoBounds{640, 360})
				}
			}
			r.ReadVideoLoop("publisher", slot, track)
			if tap.count() != 5 {
				t.Fatalf("forwarded=%d want=5", tap.count())
			}
			for i, seq := range []uint16{1, 4, 5, 6, 8} {
				if tap.packets[i].SequenceNumber != seq {
					t.Fatalf("unexpected packet %d", tap.packets[i].SequenceNumber)
				}
			}
		})
	}
}

func TestRuntimeVideoBoundsRejectExistingUninspectedCodec(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	track := &updatingVideoTrack{boundsVideoTrack: boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsKeyPacket(2, 2, 640, 360),
	}}}, mime: webrtc.MimeTypeH264}}
	track.beforeRead = func(read int) {
		if read == 1 {
			setRuntimeVideoLimits(t, r, 0, VideoBounds{640, 360})
		}
	}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 1 {
		t.Fatal("existing non-VP8 track crossed newly enabled bounds")
	}
}

func TestRuntimeVideoBoundsRenegotiatesExistingCodec(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	track := &updatingVideoTrack{boundsVideoTrack: boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsKeyPacket(2, 2, 640, 360), boundsKeyPacket(3, 3, 640, 360),
	}}}, mime: webrtc.MimeTypeH264}}
	track.beforeRead = func(read int) {
		if read == 1 {
			setRuntimeVideoLimits(t, r, 0, VideoBounds{640, 360})
		}
		if read == 2 {
			track.mime = webrtc.MimeTypeVP8
		}
	}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 2 || tap.packets[0].SequenceNumber != 1 || tap.packets[1].SequenceNumber != 3 {
		t.Fatal("must drop forbidden codec packet 2 and resume only inspected VP8 packet 3")
	}
}

func TestVideoLimitChangeInsideGuardDropsOldPacket(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	r.SetMediaGuard(func(_ MediaDelivery, write func() error) error {
		if err := r.SetVideoBitrateLimit(8000); err != nil {
			return err
		}
		return write()
	})
	if sent := r.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(1, 1, 640, 360)); sent != 0 || tap.count() != 0 {
		t.Fatal("packet checked before limit change was delivered after it")
	}
	if sent := r.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(2, 2, 640, 360)); sent != 1 {
		t.Fatal("unchanged setting invalidated a current packet")
	}
}

type heldVideoWriter struct{ entered, release chan struct{} }

func (w *heldVideoWriter) WriteRTP(*rtp.Packet) error { close(w.entered); <-w.release; return nil }

func TestVideoLimitChangeDrainsEarlierWrite(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	w := &heldVideoWriter{make(chan struct{}), make(chan struct{})}
	r.addVideoOutput("recorder", w)
	forwarded := make(chan int, 1)
	go func() { forwarded <- r.ForwardVideo("publisher", SlotCam, "", boundsKeyPacket(1, 1, 640, 360)) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	released := false
	defer func() {
		if !released {
			close(w.release)
		}
	}()
	changed := make(chan error, 1)
	go func() { changed <- r.SetVideoBitrateLimit(8000) }()
	select {
	case err := <-changed:
		t.Fatalf("change returned before old write completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(w.release)
	released = true
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("limit change blocked")
	}
	select {
	case sent := <-forwarded:
		if sent != 1 {
			t.Fatal("earlier write lost")
		}
	case <-time.After(time.Second):
		t.Fatal("writer blocked")
	}
}
