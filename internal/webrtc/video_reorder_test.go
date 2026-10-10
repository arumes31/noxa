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

func TestVideoReorderPreservesHighBitrateRepairWindow(t *testing.T) {
	for _, test := range []struct {
		name    string
		bitrate int
		first   uint16
	}{
		{name: "8.7Mbps", bitrate: 8_700_000, first: 1},
		{name: "20Mbps", bitrate: 20_000_000, first: 1},
		{name: "50Mbps sequence wrap", bitrate: 50_000_000, first: 65000},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const packetBytes = 1200
			const repairDelay = 150 * time.Millisecond // 100 ms NACK cadence plus a normal RTT.
			interval := time.Second * packetBytes * 8 / time.Duration(test.bitrate)
			count := int(repairDelay / interval)
			packetsPerFrame := max(1, test.bitrate/(60*packetBytes*8))
			now := time.Unix(1, 0)
			var queue videoReorder
			inspector := vp8BoundsInspector{bounds: VideoBounds{1920, 1080}}
			var delivered, decodable []uint16
			emit := func(packet *rtp.Packet, _ string) {
				delivered = append(delivered, packet.SequenceNumber)
				if inspector.accept(packet) {
					decodable = append(decodable, packet.SequenceNumber)
				}
			}
			keyframe := boundsKeyPacket(test.first, 1, 1920, 1080)
			keyframe.Marker = true
			queue.push(keyframe, "video/VP8", now, emit)
			packet := func(index int) *rtp.Packet {
				frame, offset := index/packetsPerFrame, index%packetsPerFrame
				payload := make([]byte, packetBytes-12)
				if offset == 0 {
					payload[0] = 0x10 // First VP8 partition starts here.
				}
				payload[1] = 0x11 // Delta-frame tag.
				return &rtp.Packet{Header: rtp.Header{
					Version: 2, SSRC: 42, SequenceNumber: test.first + 1 + uint16(index&0xffff),
					Timestamp: uint32((frame+1)*1500 + 1), Marker: offset == packetsPerFrame-1,
				}, Payload: payload}
			}
			// Lose the first delta-frame packet, but continue receiving a normal
			// high-bitrate 60 fps stream while its retransmission is in flight.
			for index := 1; index <= count; index++ {
				queue.push(packet(index), "video/VP8", now.Add(time.Duration(index)*interval), emit)
			}
			queue.push(packet(0), "video/VP8", now.Add(repairDelay), emit)
			if len(delivered) != count+2 || len(decodable) != count+2 {
				t.Fatalf("repair within %v window discarded at %s: delivered %d/%d packets, decodable %d/%d",
					repairDelay, test.name, len(delivered), count+2, len(decodable), count+2)
			}
			for index, sequence := range delivered {
				if want := test.first + uint16(index&0xffff); sequence != want {
					t.Fatalf("repaired packet order at %d: got %d, want %d", index, sequence, want)
				}
			}
		})
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

func TestVideoReorderDeadlineFollowsOldestRemainingGap(t *testing.T) {
	var queue videoReorder
	now := time.Unix(1, 0)
	var delivered []uint16
	emit := func(packet *rtp.Packet, _ string) { delivered = append(delivered, packet.SequenceNumber) }
	queue.push(boundsDeltaPacket(1, 1), "video/VP8", now, emit)
	queue.push(boundsDeltaPacket(3, 3), "video/VP8", now, emit)
	queue.push(boundsDeltaPacket(5, 5), "video/VP8", now.Add(50*time.Millisecond), emit)
	if !queue.deadline().Equal(now.Add(videoReorderWait)) {
		t.Fatal("newer buffered packet postponed the original gap deadline")
	}
	queue.push(boundsDeltaPacket(2, 2), "video/VP8", now.Add(100*time.Millisecond), emit)
	if !queue.deadline().Equal(now.Add(50*time.Millisecond + videoReorderWait)) {
		t.Fatal("repaired oldest gap retained its deadline for a newer gap")
	}
	queue.flush(now.Add(videoReorderWait), false, emit)
	if len(delivered) != 3 {
		t.Fatal("newer gap expired at the already repaired gap's deadline")
	}
	queue.flush(now.Add(50*time.Millisecond+videoReorderWait), false, emit)
	if len(delivered) != 4 || delivered[3] != 5 || !queue.deadline().IsZero() {
		t.Fatalf("expired gap did not clear its deadline: %v, %v", delivered, queue.deadline())
	}
	queue.push(boundsDeltaPacket(7, 7), "video/VP8", now.Add(time.Second), emit)
	if !queue.deadline().Equal(now.Add(time.Second + videoReorderWait)) {
		t.Fatal("new gap inherited an earlier repair window")
	}
}

func TestVideoReorderPressureRemainsBounded(t *testing.T) {
	for _, test := range []struct {
		name         string
		payloadBytes int
	}{
		{name: "packet count", payloadBytes: 1},
		{name: "byte count", payloadBytes: 65_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var queue videoReorder
			now := time.Unix(1, 0)
			delivered := 0
			emit := func(*rtp.Packet, string) { delivered++ }
			queue.push(boundsDeltaPacket(0, 1), "video/VP8", now, emit)
			payload := make([]byte, test.payloadBytes)
			for sequence := 2; sequence <= 2*videoReorderPackets; sequence++ {
				packet := &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(sequence & 0xffff)}, Payload: payload}
				queue.push(packet, "video/VP8", now, emit)
				if len(queue.pending) > videoReorderPackets || queue.bytes > videoReorderBytes {
					t.Fatal("pressure exceeded a hard storage bound")
				}
			}
			if delivered <= 1 {
				t.Fatal("exhausted queue never released its oldest gap")
			}
			queue.flush(now.Add(videoReorderWait), false, emit)
			if queue.bytes != 0 || len(queue.pending) != 0 || !queue.deadline().IsZero() {
				t.Fatal("pressure cleanup retained storage or a stale deadline")
			}
		})
	}
}

func BenchmarkVideoReorderBufferedDeadline(b *testing.B) {
	var queue videoReorder
	now := time.Unix(1, 0)
	emit := func(*rtp.Packet, string) {}
	queue.push(boundsDeltaPacket(0, 0), "video/VP8", now, emit)
	for sequence := 2; sequence < min(1026, videoReorderPackets); sequence++ {
		queue.push(boundsDeltaPacket(uint16(sequence), uint32(sequence)), "video/VP8", now, emit)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if queue.deadline().IsZero() {
			b.Fatal("buffered gap has no deadline")
		}
	}
}

func BenchmarkVideoReorderExpiredSparseWindow(b *testing.B) {
	now := time.Unix(1, 0)
	emit := func(*rtp.Packet, string) {}
	b.ReportAllocs()
	for b.Loop() {
		var queue videoReorder
		queue.push(boundsDeltaPacket(0, 0), "video/VP8", now, emit)
		for index := 1; index <= videoReorderPackets; index++ {
			sequence := uint16((index * 2) & 0xffff)
			queue.push(boundsDeltaPacket(sequence, uint32(index)), "video/VP8", now, emit)
		}
		queue.flush(now.Add(videoReorderWait), false, emit)
		if len(queue.pending) != 0 {
			b.Fatal("expired sparse window retained packets")
		}
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
	}, nil)
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
	}, nil)
	if len(sequences) != 3 || sequences[1] != 2 || sequences[2] != 3 {
		t.Fatalf("already queued RTX was discarded at timeout: %v", sequences)
	}
}
