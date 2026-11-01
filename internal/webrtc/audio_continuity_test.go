package webrtc

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func TestAudioContinuityOffsetsAndOwnership(t *testing.T) {
	now := time.Now()
	var a audioContinuity
	packet := func(ssrc uint32, seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: seq, Timestamp: ts}, Payload: []byte{0x12, 0x34}}
	}
	emit := func(p *rtp.Packet, epoch uint64, at time.Time) *rtp.Packet {
		t.Helper()
		original := p.Clone()
		out, ok := a.translate(p, epoch, 48000, at, 0, false)
		if !ok {
			t.Fatal("valid audio rejected")
		}
		if p.SequenceNumber != original.SequenceNumber || p.Timestamp != original.Timestamp || !bytes.Equal(p.Payload, original.Payload) || !bytes.Equal(out.Payload, original.Payload) {
			t.Fatal("source or ciphertext mutated")
		}
		return out
	}
	emit(packet(100, 65534, 0xffff0000), 10, now)
	emit(packet(100, 65535, 0xffff03c0), 10, now.Add(20*time.Millisecond))
	before := emit(packet(100, 0, 0xffff0780), 10, now.Add(40*time.Millisecond))
	for range 2 {
		out := emit(packet(100, 65535, 0xffff03c0), 10, now.Add(50*time.Millisecond))
		if out.SequenceNumber != 65535 || out.Timestamp != 0xffff03c0 {
			t.Fatal("reorder/duplicate lost source identity")
		}
	}
	// The ownership epoch, not only SSRC, also handles a peer reusing an SSRC.
	fresh := emit(packet(100, 65000, 1000), 11, now.Add(140*time.Millisecond))
	if fresh.SequenceNumber != before.SequenceNumber+1 || fresh.Timestamp-before.Timestamp != 4800 {
		t.Fatalf("bad codec-rate bridge: %+v -> %+v", before.Header, fresh.Header)
	}
	out := emit(packet(100, 65002, 2920), 11, now.Add(180*time.Millisecond))
	reordered := emit(packet(100, 65001, 1960), 11, now.Add(190*time.Millisecond))
	if reordered.SequenceNumber != fresh.SequenceNumber+1 || out.SequenceNumber != fresh.SequenceNumber+2 || reordered.Timestamp != fresh.Timestamp+960 {
		t.Fatal("replacement reordering renumbered")
	}
	for _, tc := range []struct {
		p     *rtp.Packet
		epoch uint64
	}{{packet(100, 1, 0xffff0b40), 10}, {packet(200, 65003, 3880), 11}, {packet(100, 64999, 40), 11}} {
		if _, ok := a.translate(tc.p, tc.epoch, 48000, now.Add(time.Second), 0, false); ok {
			t.Fatal("retired or pre-epoch packet accepted")
		}
	}
}

func TestAudioContinuityClockRateAndPreferredMapping(t *testing.T) {
	for _, rate := range []uint32{8000, 44100, 48000} {
		t.Run(strconv.FormatUint(uint64(rate), 10), func(t *testing.T) {
			now := time.Now()
			a := audioContinuity{seen: true, epoch: 1, ssrc: 100, sequence: 100, timestamp: 1_000_000, last: now}
			p := &rtp.Packet{Header: rtp.Header{SSRC: 200, SequenceNumber: 10, Timestamp: 1000}}
			out, ok := a.translate(p, 2, rate, now.Add(100*time.Millisecond), 0, false)
			if !ok || out.Timestamp != 1_000_000+rate/10 {
				t.Fatalf("clock rate %d ignored: %+v", rate, out)
			}
			// A forward publisher-clock mapping is preferred to local arrival time.
			out, ok = a.translate(&rtp.Packet{Header: rtp.Header{SSRC: 300, SequenceNumber: 10, Timestamp: 2000}}, 3, rate, now.Add(120*time.Millisecond), out.Timestamp+rate/50-2000, true)
			if !ok || out.Timestamp != 1_000_000+rate/10+rate/50 {
				t.Fatal("forward preferred mapping ignored")
			}
			previous := out.Timestamp
			out, ok = a.translate(&rtp.Packet{Header: rtp.Header{SSRC: 400, SequenceNumber: 10, Timestamp: 0}}, 4, rate, now.Add(140*time.Millisecond), previous-1, true)
			if !ok || out.Timestamp != previous+rate/50 {
				t.Fatal("backwards publisher mapping did not fall back safely")
			}
		})
	}
}

func TestAudioContinuityDeclinedAdmissionDoesNotCommit(t *testing.T) {
	track, err := pion.NewTrackLocalStaticRTP(timingOpus.RTPCodecCapability, "mic", "stream")
	if err != nil {
		t.Fatal(err)
	}
	output := &pubSlot{track: track, egress: &mediaEgressStream{registry: &mediaEgressRegistry{}}}
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 100, SequenceNumber: 200, Timestamp: 1000}, Payload: []byte{1}}
	if accepted, err := output.enqueueMedia(packet, mediaTicket{delivery: MediaDelivery{Slot: SlotMic, SourceEpoch: 10}}); accepted || err != nil {
		t.Fatalf("inactive output accepted: %v %v", accepted, err)
	}
	if output.audioContinuity.seen {
		t.Fatal("failed admission advanced recipient continuity")
	}
}

func TestAudioContinuityIndependentRecipients(t *testing.T) {
	now := time.Now()
	var early, late audioContinuity
	first := &rtp.Packet{Header: rtp.Header{SSRC: 100, SequenceNumber: 30000, Timestamp: 1_000_000}, Payload: []byte{7}}
	early.translate(first, 1, 48000, now, 0, false)
	later := *first
	later.SequenceNumber += 10
	later.Timestamp += 9600
	early.translate(&later, 1, 48000, now.Add(200*time.Millisecond), 0, false)
	late.translate(first, 1, 48000, now, 0, false)
	replacement := &rtp.Packet{Header: rtp.Header{SSRC: 200, SequenceNumber: 29000, Timestamp: 1000}, Payload: []byte{8}}
	a, okA := early.translate(replacement, 2, 48000, now.Add(220*time.Millisecond), 0, false)
	b, okB := late.translate(replacement, 2, 48000, now.Add(220*time.Millisecond), 0, false)
	if !okA || !okB || a.SequenceNumber != 30011 || b.SequenceNumber != 30001 || a.Timestamp != b.Timestamp || replacement.SequenceNumber != 29000 || replacement.Timestamp != 1000 {
		t.Fatal("recipient continuity interfered with another output or publisher")
	}
}

type audioUncertainWrite struct {
	pion.TrackLocalWriter
	headers []rtp.Header
}

func (w *audioUncertainWrite) WriteRTP(header *rtp.Header, payload []byte) (int, error) {
	w.headers = append(w.headers, header.Clone())
	return len(payload), errors.New("uncertain transport write")
}

type audioWriteContext struct {
	extensionTrackContext
	writer pion.TrackLocalWriter
}

func (c audioWriteContext) WriteStream() pion.TrackLocalWriter { return c.writer }

func TestAudioContinuityWriteErrorKeepsReservedSequence(t *testing.T) {
	track, err := pion.NewTrackLocalStaticRTP(timingOpus.RTPCodecCapability, "mic", "stream")
	if err != nil {
		t.Fatal(err)
	}
	writer := new(audioUncertainWrite)
	if _, err := track.Bind(audioWriteContext{extensionTrackContext: extensionTrackContext{codec: timingOpus}, writer: writer}); err != nil {
		t.Fatal(err)
	}
	output := &pubSlot{track: track, egress: &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}}
	for i, seq := range []uint16{30000, 29000} {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: uint32(100 + i), SequenceNumber: seq, Timestamp: uint32(1000 + i)}, Payload: []byte{byte(i)}}
		if accepted, err := output.enqueueMedia(packet, mediaTicket{delivery: MediaDelivery{Slot: SlotMic, SourceEpoch: uint64(i + 1)}}); accepted || err == nil {
			t.Fatal("transport error was not retained")
		}
	}
	if len(writer.headers) != 2 || writer.headers[0].SequenceNumber != 30000 || writer.headers[1].SequenceNumber != 30001 {
		t.Fatal("uncertain write reused sequence number across source replacement")
	}
}
