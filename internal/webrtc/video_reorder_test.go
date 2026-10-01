package webrtc

import (
	"os"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestVideoBoundsAllowRepairedPacketReordering(t *testing.T) {
	r, err := NewRouterWithVideoBounds(nil, VideoBounds{1280, 720})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(1, "publisher")
	r.JoinChannel(1, "recorder")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	track := &boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsDeltaPacket(3, 3), boundsDeltaPacket(2, 2), boundsDeltaPacket(4, 4),
	}}}, mime: webrtc.MimeTypeVP8}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if tap.count() != 4 {
		t.Fatalf("repaired sequence gap still lost frames: got %d packets, want 4", tap.count())
	}
	for i, packet := range tap.packets {
		if packet.SequenceNumber != uint16(i+1) {
			t.Fatalf("packet order: got %d at %d", packet.SequenceNumber, i)
		}
	}
}

func TestVideoReorderBoundsWrapDuplicatesAndOwnership(t *testing.T) {
	var queue videoReorder
	now := time.Unix(1, 0)
	var sequences []uint16
	emit := func(packet *rtp.Packet, _ string) {
		sequences = append(sequences, packet.SequenceNumber)
		if len(packet.Payload) > 0 && packet.Payload[0] == 99 {
			t.Error("buffer retained the caller's packet memory")
		}
	}
	queue.push(boundsDeltaPacket(65534, 1), "video/VP8", now, emit)
	queued := boundsDeltaPacket(0, 3)
	queue.push(queued, "video/VP8", now, emit)
	queued.Payload[0] = 99
	queue.push(boundsDeltaPacket(0, 3), "video/VP8", now, emit)
	queue.push(boundsDeltaPacket(65535, 2), "video/VP8", now.Add(time.Millisecond), emit)
	if len(sequences) != 3 || sequences[0] != 65534 || sequences[1] != 65535 || sequences[2] != 0 {
		t.Fatalf("wrap/duplicate ordering: %v", sequences)
	}
	for i := 2; i < 1000; i += 2 {
		packet := boundsDeltaPacket(uint16(i), uint32(i))
		packet.Payload = make([]byte, 10000)
		queue.push(packet, "video/VP8", now, emit)
		if len(queue.pending) > videoReorderPackets || queue.bytes > videoReorderBytes {
			t.Fatal("unbounded reorder storage")
		}
	}
	queue.flush(now.Add(videoReorderWait), false, emit)
	if len(queue.pending) != 0 || queue.bytes != 0 {
		t.Fatal("expired gap retained packets")
	}
}

func TestVideoReorderUnrepairedGapStillRequiresSafeKeyframe(t *testing.T) {
	var queue videoReorder
	inspector := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
	now := time.Unix(1, 0)
	var sequences []uint16
	emit := func(packet *rtp.Packet, _ string) {
		if inspector.accept(packet) {
			sequences = append(sequences, packet.SequenceNumber)
		}
	}
	queue.push(boundsKeyPacket(1, 1, 640, 360), "video/VP8", now, emit)
	queue.push(boundsDeltaPacket(3, 3), "video/VP8", now, emit)
	queue.flush(now.Add(videoReorderWait-time.Millisecond), false, emit)
	if len(queue.pending) != 1 {
		t.Fatal("repair window ended early")
	}
	queue.flush(now.Add(videoReorderWait), false, emit)
	queue.push(boundsDeltaPacket(2, 2), "video/VP8", now.Add(videoReorderWait), emit)
	queue.push(boundsKeyPacket(4, 4, 1920, 1080), "video/VP8", now.Add(videoReorderWait), emit)
	queue.push(boundsDeltaPacket(5, 5), "video/VP8", now.Add(videoReorderWait), emit)
	queue.push(boundsKeyPacket(6, 6, 640, 360), "video/VP8", now.Add(videoReorderWait), emit)
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 6 {
		t.Fatalf("lost/oversized reference reached output: %v", sequences)
	}
}

type deadlineVideoTrack struct {
	boundsVideoTrack
	deadline time.Time
	timedOut bool
	repair   *rtp.Packet
}

func (t *deadlineVideoTrack) SetReadDeadline(deadline time.Time) error {
	t.deadline = deadline
	return nil
}

func (t *deadlineVideoTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	if t.reads() == 2 && !t.timedOut && !t.deadline.IsZero() {
		time.Sleep(max(0, time.Until(t.deadline)))
		t.timedOut = true
		return nil, nil, os.ErrDeadlineExceeded
	}
	if t.timedOut && !t.deadline.IsZero() {
		if t.repair != nil {
			packet := t.repair
			t.repair = nil
			return packet, nil, nil
		}
		return nil, nil, os.ErrDeadlineExceeded
	}
	return t.boundsVideoTrack.ReadRTP()
}

func TestVideoReorderDeadlineFlushesIdleScreen(t *testing.T) {
	track := &deadlineVideoTrack{boundsVideoTrack: boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsKeyPacket(3, 3, 640, 360),
	}}}}}
	var sequences []uint16
	readOrderedVideo(track, func() bool { return true }, func() string { return "video/VP8" }, func(packet *rtp.Packet, _ string) {
		sequences = append(sequences, packet.SequenceNumber)
	})
	if !track.timedOut || len(sequences) != 2 || sequences[1] != 3 || !track.deadline.IsZero() {
		t.Fatalf("idle screen did not drain/reset deadline: %v, timeout=%v", sequences, track.timedOut)
	}
}

func TestVideoReorderDrainsRTXThatArrivedDuringPrimaryRead(t *testing.T) {
	track := &deadlineVideoTrack{boundsVideoTrack: boundsVideoTrack{fakeVideoTrack: fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
		boundsKeyPacket(1, 1, 640, 360), boundsDeltaPacket(3, 3),
	}}}}, repair: boundsDeltaPacket(2, 2)}
	inspector := vp8BoundsInspector{bounds: VideoBounds{1280, 720}}
	var sequences []uint16
	readOrderedVideo(track, func() bool { return true }, func() string { return "video/VP8" }, func(packet *rtp.Packet, _ string) {
		if inspector.accept(packet) {
			sequences = append(sequences, packet.SequenceNumber)
		}
	})
	if len(sequences) != 3 || sequences[1] != 2 || sequences[2] != 3 {
		t.Fatalf("already queued RTX was discarded at timeout: %v", sequences)
	}
}
