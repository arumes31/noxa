package grpcserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noxav1 "noxa/v1"
)

func attachMessageStream(t *testing.T, c *roleDeliveryConn, id uint32) *roleMessageStream {
	t.Helper()
	s, err := c.newMessageStream()
	if err != nil {
		t.Fatal(err)
	}
	var block, wire bytes.Buffer
	enc := hpack.NewEncoder(&block)
	if err := enc.WriteField(hpack.HeaderField{Name: roleDeliveryHeader, Value: s.token, Sensitive: true}); err != nil {
		t.Fatal(err)
	}
	if err := http2.NewFramer(&wire, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: block.Bytes(), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(wire.Bytes()); err != nil {
		t.Fatal(err)
	}
	return s
}

type fencedEventsService struct {
	noxav1.UnimplementedEventsServer
	mu       sync.RWMutex
	allowed  bool
	requests chan string
	released chan error
	exited   chan struct{}
	gate     *deliveryGate
}

func (s *fencedEventsService) Subscribe(_ *noxav1.SubscribeEventsRequest, stream grpc.ServerStreamingServer[noxav1.Event]) error {
	d, err := newProtectedStreamDelivery(stream.Context(), stream.SetHeader)
	if err != nil {
		return err
	}
	defer func() {
		d.close()
		if s.exited != nil {
			s.exited <- struct{}{}
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case id := <-s.requests:
			err := func() error {
				s.mu.RLock()
				defer s.mu.RUnlock()
				if !s.allowed {
					return status.Error(codes.PermissionDenied, "revoked")
				}
				if id == "second" {
					s.gate.armed.Store(true)
				}
				message := &noxav1.Event{Id: id}
				return d.send(stream.Context(), message, func() error { return stream.Send(message) })
			}()
			s.released <- err
			if err != nil {
				return err
			}
		}
	}
}

func TestProtectedStreamRepeatedIdleCancellationCleansMappings(t *testing.T) {
	service := &fencedEventsService{allowed: true, requests: make(chan string, 1), released: make(chan error, 1), exited: make(chan struct{}, 1)}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracker := &roleDeliveryTracker{}
	server := grpc.NewServer(grpc.StatsHandler(tracker))
	noxav1.RegisterEventsServer(server, service)
	done := make(chan error, 1)
	go func() { done <- server.Serve(&roleDeliveryListener{Listener: listener, tracker: tracker}) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn := dialGRPC(t, listener.Addr().String())
	for range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		stream, err := noxav1.NewEventsClient(conn).Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		service.requests <- "idle"
		if _, err := stream.Recv(); err != nil {
			cancel()
			t.Fatal(err)
		}
		select {
		case err := <-service.released:
			if err != nil {
				cancel()
				t.Fatal(err)
			}
		case <-ctx.Done():
			cancel()
			t.Fatal("message did not finish")
		}
		cancel()
		select {
		case <-service.exited:
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled stream handler did not clean up")
		}
		tracker.mu.Lock()
		if len(tracker.conns) != 1 {
			tracker.mu.Unlock()
			t.Fatal("idle cancellation closed the reusable connection")
		}
		for _, observer := range tracker.conns {
			observer.writeMu.Lock()
			observer.pendingMu.Lock()
			remaining := len(observer.streams) + len(observer.messages) + len(observer.pending)
			observer.pendingMu.Unlock()
			observer.writeMu.Unlock()
			if remaining != 0 {
				tracker.mu.Unlock()
				t.Fatal("idle stream retained delivery state")
			}
		}
		tracker.mu.Unlock()
	}
}

func TestRoleStreamParsesPaddedDataAtEveryByteBoundary(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	s := attachMessageStream(t, c, 1)
	f, err := c.armMessage(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	// Compressed framing uses the same length rule; no decompression is needed.
	if err := http2.NewFramer(&wire, nil).WriteDataPadded(1, false, []byte{1, 0, 0, 0, 3, 'a', 'b', 'c'}, []byte{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	for _, b := range wire.Bytes() {
		if _, err := c.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if !f.delivered || c.dataFrame != nil || len(c.buffer) != 0 {
		t.Fatal("padded frame failed to complete or retained DATA")
	}
	if _, err := c.armMessage(s); err != nil {
		t.Fatal(err)
	}
	// A one-byte padded frame cannot claim two bytes of padding.
	if _, err := c.Write([]byte{0, 0, 1, 0, 8, 0, 0, 0, 1, 2}); err == nil || !c.closed.Load() {
		t.Fatal("invalid padding accepted")
	}
}

func TestProtectedStreamLeaseCoversEachSocketWrite(t *testing.T) {
	for _, cancelPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery", true: "cancel"}[cancelPending], func(t *testing.T) {
			gate := &deliveryGate{entered: make(chan struct{}, 1), release: make(chan struct{}), closed: make(chan struct{})}
			service := &fencedEventsService{allowed: true, requests: make(chan string, 1), released: make(chan error, 3), gate: gate}
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			tracker := &roleDeliveryTracker{}
			server := grpc.NewServer(grpc.StatsHandler(tracker))
			noxav1.RegisterEventsServer(server, service)
			done := make(chan error, 1)
			go func() {
				done <- server.Serve(&roleDeliveryListener{Listener: &deliveryGateListener{Listener: listener, gate: gate}, tracker: tracker})
			}()
			t.Cleanup(func() {
				server.Stop()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			conn := dialGRPC(t, listener.Addr().String())
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream, err := noxav1.NewEventsClient(conn).Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
			if err != nil {
				t.Fatal(err)
			}
			service.requests <- "first"
			message, err := stream.Recv()
			if err != nil || message.GetId() != "first" {
				t.Fatalf("first message: %v %v", message, err)
			}
			select {
			case err := <-service.released:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first message retained lease until stream end")
			}
			service.requests <- "second"
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("second message did not reach socket")
			}
			revoked := make(chan struct{})
			go func() {
				service.mu.Lock()
				service.allowed = false
				service.mu.Unlock()
				close(revoked)
			}()
			select {
			case <-revoked:
				t.Fatal("revocation passed an unfinished socket write")
			case <-time.After(20 * time.Millisecond):
			}
			if cancelPending {
				cancel()
			} else {
				close(gate.release)
			}
			select {
			case err := <-service.released:
				if (err != nil) != cancelPending {
					t.Fatalf("second message outcome: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("second message retained lease after completion/cancellation")
			}
			select {
			case <-revoked:
			case <-time.After(time.Second):
				t.Fatal("revocation did not finish")
			}
			if cancelPending {
				select {
				case <-gate.closed:
				default:
					t.Fatal("cancelled delivery released lease without closing socket")
				}
				return
			}
			message, err = stream.Recv()
			if err != nil || message.GetId() != "second" {
				t.Fatalf("second message: %v %v", message, err)
			}
			service.requests <- "third"
			if _, err := stream.Recv(); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("stream did not observe current authority: %v", err)
			}
		})
	}
}

func messageData(t *testing.T, c *roleDeliveryConn, id uint32, data []byte) {
	t.Helper()
	var wire bytes.Buffer
	if err := http2.NewFramer(&wire, nil).WriteData(id, false, data); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(wire.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func assertMessagePending(t *testing.T, f *roleDeliveryFence) {
	t.Helper()
	select {
	case <-f.done:
		t.Fatal("message fence released before its complete socket write")
	default:
	}
}

func TestRoleStreamFencesEachMessageWithoutEndingRPC(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	s := attachMessageStream(t, c, 1)
	other := attachMessageStream(t, c, 3)
	otherFence, err := c.armMessage(other)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f, err := c.armMessage(s)
		if err != nil {
			t.Fatal(err)
		}
		// The gRPC prefix and message are both split across HTTP/2 DATA frames.
		messageData(t, c, 1, []byte{0, 0})
		assertMessagePending(t, f)
		messageData(t, c, 1, []byte{0, 0, 3, 'a', 'b'})
		assertMessagePending(t, f)
		assertMessagePending(t, otherFence)
		messageData(t, c, 1, []byte{'c'})
		select {
		case <-f.done:
			if !f.delivered {
				t.Fatal("complete message reported as aborted")
			}
		default:
			t.Fatal("message required END_STREAM to release its fence")
		}
		c.expire(f.token)
		if c.closed.Load() {
			t.Fatal("late message deadline closed healthy stream")
		}
	}
	messageData(t, c, 3, []byte{0, 0, 0, 0, 0})
	if !otherFence.delivered {
		t.Fatal("empty message not completed")
	}
}

func TestRoleStreamResetReleasesPendingMessageAsFailure(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	s := attachMessageStream(t, c, 1)
	f, err := c.armMessage(s)
	if err != nil {
		t.Fatal(err)
	}
	messageData(t, c, 1, []byte{0, 0, 0, 0, 3, 'a'})
	var wire bytes.Buffer
	if err := http2.NewFramer(&wire, nil).WriteRSTStream(1, http2.ErrCodeCancel); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(wire.Bytes()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
		if f.delivered {
			t.Fatal("reset reported complete message")
		}
	default:
		t.Fatal("reset retained message lease")
	}
	if _, err := c.armMessage(s); err == nil {
		t.Fatal("reset stream accepted another protected message")
	}
}

func TestRoleStreamMalformedMessageClosesBeforeReleasingFence(t *testing.T) {
	for _, kind := range []string{"bad compression", "oversize", "extra message", "short socket write"} {
		t.Run(kind, func(t *testing.T) {
			sink := &deliverySink{}
			c := newRoleDeliveryConn(sink)
			s := attachMessageStream(t, c, 1)
			f, err := c.armMessage(s)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte{0, 0, 0, 0, 3, 'a', 'b', 'c'}
			switch kind {
			case "bad compression":
				payload[0] = 2
			case "oversize":
				binary.BigEndian.PutUint32(payload[1:5], maxProtectedGRPCResponseBytes+1)
			case "extra message":
				payload = append(payload, 0, 0, 0, 0, 0)
			case "short socket write":
				sink.limit = 12
			}
			var wire bytes.Buffer
			if err := http2.NewFramer(&wire, nil).WriteData(1, false, payload); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Write(wire.Bytes()); err == nil {
				t.Fatal("invalid/unprotected output accepted")
			}
			if !sink.closed.Load() {
				t.Fatal("malformed output did not close transport")
			}
			select {
			case <-f.done:
				if kind != "extra message" && f.delivered {
					t.Fatal("incomplete/malformed message reported delivered")
				}
			default:
				t.Fatal("socket failure retained fence")
			}
		})
	}
}

func TestProtectedStreamSendFailureAndPanicCloseTransport(t *testing.T) {
	for _, panicSend := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicSend], func(t *testing.T) {
			sink := &deliverySink{}
			c := newRoleDeliveryConn(sink)
			s := attachMessageStream(t, c, 1)
			d := &protectedStreamDelivery{conn: c, state: s}
			func() {
				defer func() {
					if recovered := recover(); (recovered != nil) != panicSend {
						t.Fatalf("unexpected panic outcome: %v", recovered)
					}
				}()
				err := d.send(t.Context(), &noxav1.Event{}, func() error {
					messageData(t, c, 1, []byte{0, 0, 0, 0, 2, 'a'})
					if panicSend {
						panic("test send panic")
					}
					return errors.New("test partial send failure")
				})
				if err == nil {
					t.Fatal("failed send reported success")
				}
			}()
			if !sink.closed.Load() || len(c.pending) != 0 || len(c.messages) != 0 {
				t.Fatal("send failure released callback without closing/cleaning transport")
			}
		})
	}
}

func TestProtectedStreamIdleCloseRetiresSocketMapping(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	for id := uint32(1); id < 100; id += 2 {
		s := attachMessageStream(t, c, id)
		f, err := c.armMessage(s)
		if err != nil {
			t.Fatal(err)
		}
		messageData(t, c, id, []byte{0, 0, 0, 0, 0})
		<-f.done
		// Peer cancellation need not produce an outbound RST/END_STREAM.
		(&protectedStreamDelivery{conn: c, state: s}).close()
		if len(c.streams) != 0 || len(c.messages) != 0 || len(c.pending) != 0 {
			t.Fatal("idle stream cleanup retained socket mapping")
		}
	}
}

func TestRoleStreamDoesNotBufferDATAContent(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	s := attachMessageStream(t, c, 1)
	f, err := c.armMessage(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := http2.NewFramer(&wire, nil).WriteData(1, false, []byte{0, 0, 0, 0, 4, 's', 'a', 'f', 'e'}); err != nil {
		t.Fatal(err)
	}
	cut := wire.Len() - 2
	if _, err := c.Write(wire.Bytes()[:cut]); err != nil {
		t.Fatal(err)
	}
	assertMessagePending(t, f)
	if len(c.buffer) != 0 || c.reader.Size() != 0 {
		t.Fatal("partial DATA content retained in outer frame parser")
	}
	if _, err := c.Write(wire.Bytes()[cut:]); err != nil {
		t.Fatal(err)
	}
	if !f.delivered || len(c.buffer) != 0 || c.reader.Size() != 0 {
		t.Fatal("completed DATA content retained in outer frame parser")
	}
}

func TestProtectedStreamIdleCleanupDoesNotWaitForOtherSocketWrites(t *testing.T) {
	gate := &deliveryGate{entered: make(chan struct{}, 1), release: make(chan struct{}), closed: make(chan struct{})}
	c := newRoleDeliveryConn(&deliveryGateConn{Conn: &deliverySink{}, gate: gate})
	s := attachMessageStream(t, c, 1)
	d := &protectedStreamDelivery{conn: c, state: s}
	gate.armed.Store(true)
	var wire bytes.Buffer
	if err := http2.NewFramer(&wire, nil).WriteData(3, false, nil); err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{})
	go func() {
		_, _ = c.Write(wire.Bytes())
		close(written)
	}()
	<-gate.entered
	closed := make(chan struct{})
	go func() { d.close(); close(closed) }()
	defer func() {
		_ = c.Close()
		<-written
		<-closed
	}()
	select {
	case <-closed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("idle stream cleanup waited for unrelated socket I/O")
	}
	if c.closed.Load() {
		t.Fatal("idle cleanup interrupted another stream's write")
	}
}
