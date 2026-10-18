package webrtc

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestBlockedMediaWriterDoesNotBlockOtherViewerAdmission(t *testing.T) {
	slow, stream, packet, _ := terminalDiagnosticFixture(t, mediaTicket{})
	healthy, healthyStream, healthyPacket, healthyWrites := terminalDiagnosticFixture(t, mediaTicket{})
	entered, release := make(chan struct{}), make(chan struct{})
	writerDone, admissionDone := make(chan struct{}), make(chan struct{})
	defer func() { close(release); <-writerDone; <-admissionDone }()
	slow.streams[11].writer = interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		close(entered)
		<-release
		return len(payload), nil
	})
	go func() { defer close(writerDone); flushTerminalDiagnosticPacket(t, slow, packet) }()
	<-entered
	forwarded := make(chan bool, 1)
	go func() {
		defer close(admissionDone)
		// Production fan-out admits recipients sequentially. A previous socket
		// write of the first peer must not stall admission to a healthy peer.
		next, ok := stream.prepare(&packet, mediaTicket{})
		if !ok {
			forwarded <- false
			return
		}
		_, _ = slow.Write(&next.Header, next.Payload, nil)
		next, ok = healthyStream.prepare(&healthyPacket, mediaTicket{})
		if ok {
			flushTerminalDiagnosticPacket(t, healthy, next)
		}
		forwarded <- ok
	}()
	select {
	case ok := <-forwarded:
		if !ok || healthyWrites.Load() != 1 {
			t.Fatal("healthy viewer did not receive its admitted packet")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked viewer socket also blocked sequential healthy-viewer admission")
	}
}

func TestBlockedMediaWriteRetainsLifecycleRevocationLease(t *testing.T) {
	_, stream, packet, _ := terminalDiagnosticFixture(t, mediaTicket{})
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _ = stream.writeResult(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			close(entered)
			<-release
			return len(payload), nil
		}))
	}()
	<-entered
	// Assert the actual lease, rather than depending on whether the stop
	// goroutine below has been scheduled before its nonblocking check.
	if stream.mu.TryLock() {
		stream.mu.Unlock()
		close(release)
		<-finished
		t.Fatal("authorized socket write did not retain its lifecycle read lease")
	}
	stopping, stopped := make(chan struct{}), make(chan struct{})
	go func() { close(stopping); stream.stop(); close(stopped) }()
	<-stopping
	select {
	case <-stopped:
		close(release)
		<-finished
		t.Fatal("stream revocation crossed an outstanding authorized write")
	default:
	}
	close(release)
	<-finished
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("completed socket write retained the lifecycle lease")
	}
	if _, ok := stream.prepare(&packet, mediaTicket{}); ok {
		t.Fatal("retired publication admitted a new ticket")
	}
	_, reason, err := stream.writeResult(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		t.Fatal("old repair crossed publication revocation")
		return 0, nil
	}))
	if err != nil || reason != mediaDropInactiveStream {
		t.Fatalf("retired write result: %v %v", reason, err)
	}
}

func TestBlockedMediaWriteAllowsBoundedTicketEvictionWithoutRepairReuse(t *testing.T) {
	_, stream, packet, _ := terminalDiagnosticFixture(t, mediaTicket{})
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _ = stream.writeResult(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			close(entered)
			<-release
			return len(payload), nil
		}))
	}()
	<-entered
	admitted := make(chan bool, 1)
	go func() {
		for range mediaTicketCount {
			if _, ok := stream.prepare(&packet, mediaTicket{}); !ok {
				admitted <- false
				return
			}
		}
		admitted <- true
	}()
	select {
	case ok := <-admitted:
		if !ok {
			close(release)
			<-finished
			t.Fatal("active publication refused ticket admission")
		}
	case <-time.After(time.Second):
		close(release)
		<-finished
		<-admitted
		t.Fatal("ticket cache remained coupled to blocked socket")
	}
	stream.ticketsMu.Lock()
	count := len(stream.tickets)
	stream.ticketsMu.Unlock()
	close(release)
	<-finished
	if count != mediaTicketCount {
		t.Fatalf("ticket cache escaped its bound: %d", count)
	}
	_, reason, err := stream.writeResult(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		t.Fatal("evicted ticket authorized a later repair")
		return 0, nil
	}))
	if err != nil || reason != mediaDropMissingTicket {
		t.Fatalf("evicted repair result: %v %v", reason, err)
	}
}
