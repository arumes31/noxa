package webrtc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestMediaEgressRetiresFailedOutput(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{{"deadline", os.ErrDeadlineExceeded}, {"closed transport", io.ErrClosedPipe}} {
		t.Run(failure.name, func(t *testing.T) {
			registry := &mediaEgressRegistry{}
			stream := &mediaEgressStream{active: true, registry: registry}
			source := &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}
			packet, _ := stream.prepare(source, mediaTicket{})
			attempts := 0
			writer := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
				attempts++
				return 0, failure.err
			})
			if _, err := stream.write(&packet.Header, packet.Payload, nil, writer); !errors.Is(err, failure.err) {
				t.Fatalf("write error = %v, want %v", err, failure.err)
			}
			// An already queued packet and a NACK reuse the original ticket.
			// Neither may revisit a transport that has failed its write budget.
			_, _ = stream.write(&packet.Header, packet.Payload, nil, writer)
			if attempts != 1 {
				t.Errorf("failed output was retried %d times", attempts)
			}
			if _, ok := stream.prepare(source, mediaTicket{}); ok {
				t.Error("failed output accepted another packet")
			}
		})
	}
}

func TestMediaEgressGuardErrorPreservesHealthyOutput(t *testing.T) {
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	denied := errors.New("authorization temporarily unavailable")
	guardError := denied
	guard := func(_ MediaDelivery, write func() error) error {
		if guardError != nil {
			return guardError
		}
		return write()
	}
	source := &rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}
	packet, _ := stream.prepare(source, mediaTicket{guard: guard})
	attempts := 0
	writer := interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		attempts++
		return len(payload), nil
	})
	if _, err := stream.write(&packet.Header, packet.Payload, nil, writer); !errors.Is(err, denied) {
		t.Fatalf("guard error = %v, want %v", err, denied)
	}
	if attempts != 0 {
		t.Fatal("guard rejection reached terminal writer")
	}
	guardError = nil
	fresh, ok := stream.prepare(source, mediaTicket{guard: guard})
	if !ok {
		t.Fatal("guard error retired a healthy output")
	}
	if n, err := stream.write(&fresh.Header, fresh.Payload, nil, writer); err != nil || n != len(fresh.Payload) || attempts != 1 {
		t.Fatalf("healthy write after guard recovery: n=%d err=%v attempts=%d", n, err, attempts)
	}
}

func TestMediaEgressSocketDeadlineReleasesAuthorizationAndPolicy(t *testing.T) {
	v := runtimeVoice(t)
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	connection, remote := net.Pipe()
	t.Cleanup(func() { _ = connection.Close(); _ = remote.Close() })
	var authority sync.RWMutex
	guard := func(_ MediaDelivery, write func() error) error {
		authority.RLock()
		defer authority.RUnlock()
		return write()
	}
	packet, _ := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}, mediaTicket{
		guard: guard, router: v.router, videoRevision: v.router.videoPolicySnapshot().revision,
		delivery: MediaDelivery{Slot: SlotCam, Tap: true},
	})
	started := make(chan struct{})
	written := make(chan error, 1)
	var gate socketWriteGate
	go func() {
		_, err := stream.write(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			return gate.write(connection.SetWriteDeadline, func() (int, error) {
				close(started)
				return connection.Write(payload)
			})
		}))
		written <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("terminal write did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := v.CommitVideoLimits(ctx, 8000, VideoBounds{640, 360}, nil); err != nil {
		t.Fatalf("stalled transport retained policy gate: %v", err)
	}
	select {
	case err := <-written:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("stalled write returned %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stalled write retained its authorization lease")
	}
	if !authority.TryLock() {
		t.Fatal("completed stalled write retained its authorization lease")
	}
	authority.Unlock()
	if _, ok := stream.prepare(&rtp.Packet{}, mediaTicket{}); ok {
		t.Fatal("stalled output remained available after its deadline")
	}
}

func TestMediaEgressPolicyCommitRejectsPacedAndNACKBacklog(t *testing.T) {
	v := runtimeVoice(t)
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	registry.streams = map[string]*mediaEgressStream{"policy": stream}
	ccI, err := (mediaCCFactory{registry: registry}).NewInterceptor("policy")
	if err != nil {
		t.Fatal(err)
	}
	nackFactory, err := nack.NewResponderInterceptor()
	if err != nil {
		t.Fatal(err)
	}
	nackI, err := nackFactory.NewInterceptor("policy")
	if err != nil {
		t.Fatal(err)
	}
	chain := interceptor.NewChain([]interceptor.Interceptor{ccI, nackI})
	t.Cleanup(func() { stream.stop(); _ = chain.Close() })
	pacer := ccI.(*mediaCCInterceptor).pacer
	pacer.SetTargetBitrate(0)
	info := &interceptor.StreamInfo{ID: "policy", SSRC: 11, SSRCRetransmission: 22, PayloadType: 96, PayloadTypeRetransmission: 97, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}
	written := make(chan rtp.Header, 8)
	writer := chain.BindLocalStream(info, interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		written <- header.Clone()
		return header.MarshalSize() + len(payload), nil
	}))
	checked := make(chan struct{}, 8)
	guard := func(_ MediaDelivery, write func() error) error {
		defer func() { checked <- struct{}{} }()
		return write()
	}
	send := func(sequence uint16) {
		t.Helper()
		packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 11, SequenceNumber: sequence, PayloadType: 96}, Payload: []byte{1}}, mediaTicket{
			guard: guard, router: v.router, videoRevision: v.router.videoPolicySnapshot().revision,
			delivery: MediaDelivery{Slot: SlotCam, Tap: true},
		})
		if !ok {
			t.Fatal("ticket refused")
		}
		if _, err := writer.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	awaitCheck := func() {
		t.Helper()
		select {
		case <-checked:
		case <-time.After(time.Second):
			t.Fatal("packet did not reach terminal policy check")
		}
	}
	assertNoWrite := func() {
		t.Helper()
		awaitCheck()
		select {
		case header := <-written:
			t.Fatalf("obsolete policy packet emitted: %+v", header)
		default:
		}
	}
	assertWrite := func(ssrc uint32) {
		t.Helper()
		awaitCheck()
		select {
		case header := <-written:
			if header.SSRC != ssrc {
				t.Fatalf("SSRC = %d, want %d", header.SSRC, ssrc)
			}
		default:
			t.Fatal("current policy packet was discarded")
		}
	}
	request := func(sequence uint16) {
		t.Helper()
		raw, err := (&rtcp.TransportLayerNack{SenderSSRC: 99, MediaSSRC: 11, Nacks: []rtcp.NackPair{{PacketID: sequence}}}).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		reader := chain.BindRTCPReader(interceptor.RTCPReaderFunc(func(buffer []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			return copy(buffer, raw), attrs, nil
		}))
		if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
	}
	send(5)
	if err := v.CommitVideoLimits(t.Context(), 8000, VideoBounds{640, 360}, nil); err != nil {
		t.Fatal(err)
	}
	pacer.SetTargetBitrate(1_500_000)
	assertNoWrite()
	send(6)
	assertWrite(11)
	request(5)
	assertNoWrite()
	request(6)
	assertWrite(22)
	chain.UnbindLocalStream(info)
}
