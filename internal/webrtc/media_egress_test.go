package webrtc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"go.uber.org/zap"
)

func TestMediaEgressGuardsActualPacedAndRetransmittedPackets(t *testing.T) {
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	registry.streams = map[string]*mediaEgressStream{"test": stream}
	ccI, err := (mediaCCFactory{registry: registry}).NewInterceptor("test")
	if err != nil {
		t.Fatal(err)
	}
	nackFactory, err := nack.NewResponderInterceptor()
	if err != nil {
		t.Fatal(err)
	}
	nackI, err := nackFactory.NewInterceptor("test")
	if err != nil {
		t.Fatal(err)
	}
	chain := interceptor.NewChain([]interceptor.Interceptor{ccI, nackI})
	t.Cleanup(func() { stream.stop(); _ = chain.Close() })
	info := &interceptor.StreamInfo{ID: "test", SSRC: 11, SSRCRetransmission: 22, PayloadType: 96, PayloadTypeRetransmission: 97, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}
	written := make(chan rtp.Header, 4)
	writer := chain.BindLocalStream(info, interceptor.RTPWriterFunc(func(h *rtp.Header, p []byte, _ interceptor.Attributes) (int, error) {
		written <- h.Clone()
		return h.MarshalSize() + len(p), nil
	}))
	var revoked atomic.Bool
	checked := make(chan struct{}, 4)
	guard := func(_ MediaDelivery, write func() error) error {
		defer func() { checked <- struct{}{} }()
		if revoked.Load() {
			return nil
		}
		return write()
	}
	source := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 11, PayloadType: 96, SequenceNumber: 5, Timestamp: 9000, CSRC: []uint32{123}}, Payload: []byte{1, 2, 3}}
	packet, ok := stream.prepare(source, mediaTicket{guard: guard})
	if !ok {
		t.Fatal("ticket refused")
	}
	source.CSRC[0] = 999
	if _, err = writer.Write(&packet.Header, packet.Payload, nil); err != nil {
		t.Fatal(err)
	}
	assertWrite := func(ssrc uint32) {
		t.Helper()
		select {
		case header := <-written:
			if header.SSRC != ssrc || len(header.CSRC) != 1 || header.CSRC[0] != 123 {
				t.Fatalf("internal ticket leaked or RTX lost: %+v", header)
			}
		case <-time.After(time.Second):
			t.Fatal("missing RTP")
		}
		<-checked
	}
	assertWrite(11)
	raw, err := (&rtcp.TransportLayerNack{SenderSSRC: 99, MediaSSRC: 11, Nacks: []rtcp.NackPair{{PacketID: 5}}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reader := chain.BindRTCPReader(interceptor.RTCPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		return copy(b, raw), a, nil
	}))
	request := func() {
		t.Helper()
		if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
	}
	request()
	assertWrite(22)
	revoked.Store(true)
	request()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("NACK did not cross terminal authority")
	}
	select {
	case <-written:
		t.Fatal("revoked retransmission emitted")
	default:
	}
	chain.UnbindLocalStream(info)
}

func TestMediaEgressDropsOldTicketsAcrossPolicyAndStreamChanges(t *testing.T) {
	for _, change := range []string{"video", "evicted", "expired", "stopped"} {
		t.Run(change, func(t *testing.T) {
			registry := &mediaEgressRegistry{}
			stream := &mediaEgressStream{active: true, registry: registry}
			r := NewRouter(zap.NewNop())
			policy := r.videoPolicySnapshot()
			ticket := mediaTicket{router: r, videoRevision: policy.revision, delivery: MediaDelivery{Slot: SlotCam, Tap: true}}
			source := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1}, Payload: []byte{1}}
			packet, _ := stream.prepare(source, ticket)
			switch change {
			case "video":
				if err := r.SetVideoLimits(123, VideoBounds{}); err != nil {
					t.Fatal(err)
				}
			case "evicted":
				for range mediaTicketCount {
					stream.prepare(source, ticket)
				}
			case "expired":
				stream.tickets[1].expires = time.Now().Add(-time.Second)
			case "stopped":
				stream.stop()
			}
			_, err := stream.write(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
				t.Error("obsolete media emitted")
				return 0, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaTicketsAreBoundedPerStream(t *testing.T) {
	registry := &mediaEgressRegistry{}
	first := &mediaEgressStream{active: true, registry: registry}
	other := &mediaEgressStream{active: true, registry: registry}
	source := &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}
	packet, _ := first.prepare(source, mediaTicket{})
	for range mediaTicketCount - 1 {
		other.prepare(source, mediaTicket{})
	}
	first.prepare(source, mediaTicket{})
	written := false
	_, err := first.write(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { written = true; return 1, nil }))
	if err != nil || !written {
		t.Fatalf("other streams evicted this stream's ticket: %v", err)
	}
}
