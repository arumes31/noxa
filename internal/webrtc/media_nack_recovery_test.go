package webrtc

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestEngineNACKStopsObsoleteRetriesAndRequestsFreshLoss(t *testing.T) {
	_, registry, err := newEngineMedia(false, VideoBounds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.Build("bounded-nack-recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	requests := make(chan uint16, 128)
	chain.BindRTCPWriter(interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
		for _, packet := range packets {
			if nack, ok := packet.(*rtcp.TransportLayerNack); ok && nack.MediaSSRC == 42 {
				for _, pair := range nack.Nacks {
					pair.Range(func(sequence uint16) bool { requests <- sequence; return true })
				}
			}
		}
		return len(packets), nil
	}))
	var incoming []byte
	reader := chain.BindRemoteStream(&interceptor.StreamInfo{SSRC: 42, ClockRate: 90000, MimeType: "video/VP8",
		RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}},
		interceptor.RTPReaderFunc(func(buffer []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			return copy(buffer, incoming), attrs, nil
		}))
	receive := func(sequence uint16) {
		t.Helper()
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: sequence, Timestamp: uint32(sequence) * 9000}, Payload: []byte{0x10, 1}}
		incoming, err = packet.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
	}
	receive(100)
	receive(102)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for range 10 {
		select {
		case sequence := <-requests:
			if sequence != 101 {
				t.Fatalf("requested received packet %d", sequence)
			}
		case <-deadline.C:
			t.Fatal("missing packet did not receive its recovery attempts")
		}
	}
	// Keep the source idle: advancing its packet window must not be required
	// to stop requesting a frame that can no longer be repaired usefully.
	select {
	case sequence := <-requests:
		t.Errorf("obsolete missing packet %d was requested more than 10 times", sequence)
	case <-time.After(350 * time.Millisecond):
	}
	receive(104)
	freshDeadline := time.NewTimer(time.Second)
	defer freshDeadline.Stop()
	for {
		select {
		case sequence := <-requests:
			if sequence == 103 {
				return
			}
		case <-freshDeadline.C:
			t.Fatal("exhausted old loss suppressed recovery of fresh missing packet 103")
		}
	}
}

func TestEngineNACKResponderStillRetransmits(t *testing.T) {
	_, registry, err := newEngineMedia(false, VideoBounds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.Build("nack-responder-preserved")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	written := make(chan *rtp.Packet, 4)
	writer := chain.BindLocalStream(&interceptor.StreamInfo{SSRC: 11, SSRCRetransmission: 12, PayloadType: 96,
		PayloadTypeRetransmission: 97, ClockRate: 90000, MimeType: "video/VP8", RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}},
		interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			written <- &rtp.Packet{Header: header.Clone(), Payload: append([]byte(nil), payload...)}
			return header.MarshalSize() + len(payload), nil
		}))
	payload := []byte{0x10, 1, 2, 3}
	if _, err := writer.Write(&rtp.Header{Version: 2, SSRC: 11, SequenceNumber: 200, PayloadType: 96}, payload, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("original packet did not reach the wire")
	}
	feedback, err := (&rtcp.TransportLayerNack{SenderSSRC: 99, MediaSSRC: 11, Nacks: rtcp.NackPairsFromSequenceNumbers([]uint16{200})}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reader := chain.BindRTCPReader(interceptor.RTCPReaderFunc(func(buffer []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		return copy(buffer, feedback), attrs, nil
	}))
	if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-written:
		if packet.SSRC != 12 || packet.PayloadType != 97 || len(packet.Payload) != len(payload)+2 || binary.BigEndian.Uint16(packet.Payload[:2]) != 200 || !bytes.Equal(packet.Payload[2:], payload) {
			t.Fatalf("NACK did not produce the expected RTX packet: %+v", packet)
		}
	case <-time.After(time.Second):
		t.Fatal("NACK responder no longer retransmits recoverable media")
	}
}
