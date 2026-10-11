package webrtc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

func syntheticSRTP(t *testing.T) *srtp.Context {
	t.Helper()
	// Public, fixed fixture bytes only; no connection keys or network traffic.
	context, err := srtp.CreateContext(bytes.Repeat([]byte{0x31}, 16), bytes.Repeat([]byte{0x52}, 12),
		srtp.ProtectionProfileAeadAes128Gcm, srtp.SRTPReplayProtection(64))
	if err != nil {
		t.Fatal(err)
	}
	return context
}

// Exercise the pinned responder's actual cache and OSN rewriting, the real
// asynchronous pacer, and SRTP replay protection. Requests deliberately move
// backward and repeat; cached packet sequence numbers cannot serve as RTX RSNs.
func TestMediaRTXSequencePinnedResponderAndSRTP(t *testing.T) {
	factory, err := nack.NewResponderInterceptor()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := factory.NewInterceptor("synthetic-rtx")
	if err != nil {
		t.Fatal(err)
	}
	p := newMediaPacer()
	t.Cleanup(func() { _ = responder.Close(); _ = p.Close() })
	info := &interceptor.StreamInfo{ID: "synthetic", SSRC: 1234, SSRCRetransmission: 4321,
		PayloadType: 102, PayloadTypeRetransmission: 103, MimeType: "video/H264", ClockRate: 90000,
		RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}
	encrypt, decrypt := syntheticSRTP(t), syntheticSRTP(t)
	type result struct {
		data []byte
		err  error
	}
	wire := make(chan result, 8)
	p.AddStream(info.SSRC, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		packet := rtp.Packet{Header: h.Clone(), Payload: append([]byte(nil), payload...)}
		plain, marshalErr := packet.Marshal()
		if marshalErr != nil {
			return 0, marshalErr
		}
		if h.SSRC == info.SSRCRetransmission {
			ciphertext, encryptErr := encrypt.EncryptRTP(nil, plain, nil)
			wire <- result{ciphertext, encryptErr}
			return len(plain), encryptErr
		}
		return len(plain), nil
	}))
	p.addRTX(info.SSRC, info.SSRCRetransmission)
	// Also exercise SRTP rollover, with no worker writes before this setup.
	p.rtxSequences[info.SSRCRetransmission].next = 65535
	writer := responder.BindLocalStream(info, p)
	originals := map[uint16]rtp.Packet{}
	for seq := uint16(4000); seq <= 4002; seq++ {
		packet := rtp.Packet{Header: rtp.Header{Version: 2, SSRC: info.SSRC, PayloadType: info.PayloadType,
			SequenceNumber: seq, Timestamp: 90000 + uint32(seq-4000)*1500, Marker: true,
			CSRC: []uint32{0x12345678, uint32(seq)}}, Payload: []byte{0x65, byte(seq >> 8), byte(seq), 0xde, 0xad, 0xbe, 0xef}}
		originals[seq] = *packet.Clone()
		if _, err := writer.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(packet, originals[seq]) {
			t.Fatal("admission mutated original")
		}
	}
	var incoming []byte
	reader := responder.BindRTCPReader(interceptor.RTCPReaderFunc(func(out []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		return copy(out, incoming), attrs, nil
	}))
	for index, osn := range []uint16{4002, 4000, 4000, 4001, 4000} {
		incoming, err = rtcp.Marshal([]rtcp.Packet{&rtcp.TransportLayerNack{SenderSSRC: 999, MediaSSRC: info.SSRC,
			Nacks: []rtcp.NackPair{{PacketID: osn}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
		var sent result
		select {
		case sent = <-wire:
		case <-time.After(time.Second):
			t.Fatal("missing RTX")
		}
		if sent.err != nil {
			t.Fatal(sent.err)
		}
		plain, err := decrypt.DecryptRTP(nil, sent.data, nil)
		if err != nil {
			t.Fatalf("repair %d rejected by SRTP: %v", index, err)
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(plain); err != nil {
			t.Fatal(err)
		}
		original := originals[osn]
		if packet.SequenceNumber != uint16(65535+index) || packet.SSRC != info.SSRCRetransmission || packet.PayloadType != info.PayloadTypeRetransmission {
			t.Fatalf("incorrect RTX sequence/mapping: %+v", packet.Header)
		}
		if packet.Timestamp != original.Timestamp || packet.Marker != original.Marker || !reflect.DeepEqual(packet.CSRC, original.CSRC) ||
			len(packet.Payload) < 2 || binary.BigEndian.Uint16(packet.Payload[:2]) != osn || !bytes.Equal(packet.Payload[2:], original.Payload) {
			t.Fatal("OSN, payload, timestamp, marker or authorization ticket changed")
		}
		// The same encrypted RSN really is rejected: succeeding fresh repairs
		// above is not an artifact of disabled replay detection in the fixture.
		if _, err := decrypt.DecryptRTP(nil, sent.data, nil); err == nil {
			t.Fatal("SRTP accepted replay")
		}
	}
}

func sequencePacer(writer interceptor.RTPWriter) *mediaPacer {
	p := &mediaPacer{streams: make(map[uint32]*pacedStream)}
	p.AddStream(11, writer)
	p.addRTX(11, 22)
	p.rtxSequences[22].next = 100
	return p
}

func sequencePacket(ssrc uint32) rtp.Packet {
	return rtp.Packet{Header: rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: 7, Timestamp: 90000,
		PayloadType: 103, CSRC: []uint32{1, 2}}, Payload: []byte{0, 7, 0x65, 1}}
}

func TestMediaRTXSequenceRebindPaddingFailureAndOwnership(t *testing.T) {
	var sent []rtp.Packet
	fail := true
	writeErr := errors.New("synthetic socket failure")
	writer := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		sent = append(sent, rtp.Packet{Header: h.Clone(), Payload: append([]byte(nil), payload...)})
		if fail {
			fail = false
			return 0, writeErr
		}
		return h.MarshalSize() + len(payload), nil
	})
	p := sequencePacer(writer)
	original := sequencePacket(22)
	before := *original.Clone()
	flushTerminalDiagnosticPacket(t, p, original)
	p.removeStream(11, 22)
	p.AddStream(11, writer)
	p.addRTX(11, 22)
	flushTerminalDiagnosticPacket(t, p, original)
	padding := *original.Clone()
	padding.Payload = nil
	padding.Padding, padding.PaddingSize = true, 4
	flushTerminalDiagnosticPacket(t, p, padding)
	primary := sequencePacket(11)
	flushTerminalDiagnosticPacket(t, p, primary)
	primary.Payload = nil
	primary.Padding, primary.PaddingSize = true, 4
	flushTerminalDiagnosticPacket(t, p, primary)
	if got := []uint16{sent[0].SequenceNumber, sent[1].SequenceNumber, sent[2].SequenceNumber, sent[3].SequenceNumber, sent[4].SequenceNumber}; !reflect.DeepEqual(got, []uint16{100, 101, 102, 7, 7}) {
		t.Fatalf("attempt/rebind/padding sequence: %v", got)
	}
	if !reflect.DeepEqual(original, before) || p.diagnostics.finalWriteFailures != 1 {
		t.Fatal("cache ownership or failure accounting changed")
	}
	// A legacy same-SSRC retransmission is not an independent RTX stream.
	p.AddStream(33, writer)
	p.addRTX(33, 33)
	flushTerminalDiagnosticPacket(t, p, sequencePacket(33))
	if sent[len(sent)-1].SequenceNumber != 7 || len(p.rtxSequences) != 1 {
		t.Fatal("legacy same-SSRC sequence changed")
	}
}

func TestMediaRTXSequenceAuthorizationAndCoalescing(t *testing.T) {
	allowed := false
	p, _, original, writes := terminalDiagnosticFixture(t, mediaTicket{guard: func(_ MediaDelivery, write func() error) error {
		if allowed {
			return write()
		}
		return nil
	}})
	p.rtxSequences[22].next = 300
	original.SSRC = 22
	original.Payload = []byte{0, 7, 0x65}
	flushTerminalDiagnosticPacket(t, p, original)
	if *writes != 0 || p.rtxSequences[22].next != 300 {
		t.Fatal("denied repair consumed sequence or reached writer")
	}
	allowed = true
	for range 2 {
		if _, err := p.Write(&original.Header, original.Payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	if p.count != 1 || p.diagnostics.coalescedRepairs != 1 || p.rtxSequences[22].next != 300 {
		t.Fatal("queued duplicates consumed sequence")
	}
	p.writePacket(p.popLocked())
	flushTerminalDiagnosticPacket(t, p, original)
	if *writes != 2 || p.rtxSequences[22].next != 302 {
		t.Fatal("later NACK did not receive fresh sequence")
	}
}

func TestMediaRTXSequenceCapacityRetainsExistingAndPrimary(t *testing.T) {
	var writes int
	writer := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		writes++
		return h.MarshalSize() + len(payload), nil
	})
	p := sequencePacer(writer)
	for i := uint32(1); i < mediaRTXSequenceStates; i++ {
		p.removeStream(11, 22+i-1)
		p.AddStream(11, writer)
		p.addRTX(11, 22+i)
	}
	p.removeStream(11, 22+mediaRTXSequenceStates-1)
	p.AddStream(11, writer)
	p.addRTX(11, 9000)
	packet := sequencePacket(9000)
	if n, err := p.Write(&packet.Header, packet.Payload, nil); n != 0 || !errors.Is(err, errMediaRTXSequenceLimit) {
		t.Fatalf("capacity exhaustion silently accepted: n=%d err=%v", n, err)
	}
	if p.count != 0 || p.diagnostics.droppedRetired != 1 || len(p.rtxSequences) != mediaRTXSequenceStates {
		t.Fatal("limit accounting or bounded state failed")
	}
	flushTerminalDiagnosticPacket(t, p, sequencePacket(11))
	p.removeStream(11, 9000)
	p.AddStream(11, writer)
	p.addRTX(11, 22)
	flushTerminalDiagnosticPacket(t, p, sequencePacket(22))
	if writes != 2 || p.rtxSequences[22].next != 101 {
		t.Fatal("capacity exhaustion evicted existing state or disabled primary")
	}
	other := sequencePacer(writer)
	if other.rtxSequences[22] == p.rtxSequences[22] || other.rtxSequences[22].next != 100 {
		t.Fatal("peers share SRTP sequence state")
	}
}

func TestMediaRTXSequenceCloseJoinsBeforeClearing(t *testing.T) {
	synctest.Test(t, testMediaRTXSequenceCloseJoinsBeforeClearing)
}

func testMediaRTXSequenceCloseJoinsBeforeClearing(t *testing.T) {
	p := newMediaPacer()
	entered, release := make(chan struct{}), make(chan struct{})
	p.AddStream(11, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		close(entered)
		<-release
		return h.MarshalSize() + len(payload), nil
	}))
	p.addRTX(11, 22)
	packet := sequencePacket(22)
	if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		_ = p.Close()
		t.Fatal("worker did not enter")
	}
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	synctest.Wait()
	// Explicitly acquire the worker-independent mutex while its write is
	// blocked; retained state must survive until that worker finishes.
	p.mu.Lock()
	if !p.closed || len(p.rtxSequences) != 1 {
		t.Error("state cleared before final write returned")
	}
	p.mu.Unlock()
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join")
	}
	if len(p.rtxSequences) != 0 {
		t.Fatal("closed peer retained sequence states")
	}
	p.AddStream(11, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 0, nil }))
	p.addRTX(11, 22)
	if len(p.rtxSequences) != 0 {
		t.Fatal("closed peer allocated sequence state")
	}
}

func TestMediaRTXSequenceExpiryDoesNotConsume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		p.AddStream(11, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			t.Error("expired repair reached socket")
			return 0, nil
		}))
		p.addRTX(11, 22)
		p.rtxSequences[22].next = 123
		packet := sequencePacket(22)
		if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(mediaPacerLifetime + 10*time.Millisecond)
		synctest.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.diagnostics.droppedExpired != 1 || p.count != 0 || p.rtxSequences[22].next != 123 {
			t.Fatal("expired queued repair consumed sequence or remained queued")
		}
	})
}

func TestMediaRTXSequencePacketCapturesBindingState(t *testing.T) {
	var sequence []uint16
	writer := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		sequence = append(sequence, h.SequenceNumber)
		return h.MarshalSize() + len(payload), nil
	})
	p := sequencePacer(writer)
	packet := sequencePacket(22)
	if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
		t.Fatal(err)
	}
	queued := p.popLocked()
	// Binding metadata may change after admission. The owned packet retains
	// the exact old SSRC's sequence owner, rather than rereading mutable state.
	p.addRTX(11, 23)
	p.rtxSequences[23].next = 500
	p.writePacket(queued)
	flushTerminalDiagnosticPacket(t, p, sequencePacket(23))
	if !reflect.DeepEqual(sequence, []uint16{100, 500}) || p.rtxSequences[22].next != 101 || p.rtxSequences[23].next != 501 {
		t.Fatal("packet used a changed binding's RTX sequence or mixed SSRCs")
	}
}

func TestMediaRTXSequenceInFlightRebind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		entered, release := make(chan struct{}), make(chan struct{})
		sequences := make(chan uint16, 2)
		p.AddStream(11, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			sequences <- h.SequenceNumber
			close(entered)
			<-release
			return h.MarshalSize() + len(payload), nil
		}))
		p.addRTX(11, 22)
		p.rtxSequences[22].next = 100
		packet := sequencePacket(22)
		if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
		<-entered
		p.removeStream(11, 22)
		p.AddStream(11, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			sequences <- h.SequenceNumber
			return h.MarshalSize() + len(payload), nil
		}))
		p.addRTX(11, 22)
		if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(sequences) != 1 {
			t.Error("new binding wrote concurrently with old socket write")
		}
		close(release)
		first, second := <-sequences, <-sequences
		if first != 100 || second != 101 {
			t.Fatalf("in-flight rebind reused index: %d, %d", first, second)
		}
	})
}
