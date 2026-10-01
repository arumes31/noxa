package grpcserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

type deliverySink struct {
	net.Conn
	closed atomic.Bool
	limit  int
}

func TestProtectedReadDeadlineBeforePublicationKeepsConnection(t *testing.T) {
	base, remote := net.Pipe()
	t.Cleanup(func() { _ = base.Close(); _ = remote.Close() })
	sink := &deliverySink{Conn: base}
	conn := newRoleDeliveryConn(sink)
	conn.tracker = &roleDeliveryTracker{}
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), roleDeliveryConnKey{}, conn), 20*time.Millisecond)
	defer cancel()
	releaseBackend := make(chan struct{})
	_, err := protectedUnaryRead(ctx, zap.NewNop(), func(ctx context.Context, _ func(*noxav1.ListChannelsResponse) error) error {
		<-ctx.Done()
		<-releaseBackend
		return ctx.Err()
	})
	// Keep worker cleanup out of the race: the deadline callback itself must
	// retire the unsent fence without closing the connection.
	expired := false
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		conn.pendingMu.Lock()
		expired = len(conn.pending) == 0
		conn.pendingMu.Unlock()
		if expired {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseBackend)
	conn.tracker.workers.Wait()
	if !expired {
		t.Fatal("deadline did not retire the unsent fence")
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("backend deadline = %v", err)
	}
	if sink.closed.Load() {
		t.Fatal("backend deadline closed the connection before any response was published")
	}
}

func TestProtectedReadPreservesReadyFailureDuringCleanup(t *testing.T) {
	conn := newRoleDeliveryConn(&deliverySink{})
	conn.tracker = &roleDeliveryTracker{}
	ctx := context.WithValue(t.Context(), roleDeliveryConnKey{}, conn)
	defer conn.tracker.workers.Wait()
	// Immediate failures can finish cleanup before the handler's select runs.
	// Cancellation of the private worker context must not replace the outcome.
	for range 5000 {
		_, err := protectedUnaryRead(ctx, zap.NewNop(), func(context.Context, func(*noxav1.ListChannelsResponse) error) error {
			return authorization.ErrRoleConflict
		})
		if status.Code(err) != codes.Aborted {
			t.Fatalf("cleanup replaced ready conflict: %v", err)
		}
	}
}

func (s *deliverySink) Write(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	if s.limit > 0 && len(p) > s.limit {
		return s.limit, io.ErrUnexpectedEOF
	}
	return len(p), nil
}
func (s *deliverySink) Close() error { s.closed.Store(true); return nil }

func TestRoleDeliveryObservesCompleteWrittenHeaderBlocks(t *testing.T) {
	c := newRoleDeliveryConn(&deliverySink{})
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, streamID := range []uint32{1, 3} {
		fence, err := c.register()
		if err != nil {
			t.Fatal(err)
		}
		block.Reset()
		for _, field := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-type", Value: "application/grpc"}, {Name: roleDeliveryHeader, Value: fence.token}} {
			if err := encoder.WriteField(field); err != nil {
				t.Fatal(err)
			}
		}
		var wire bytes.Buffer
		framer := http2.NewFramer(&wire, nil)
		cut := block.Len() / 2
		if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: streamID, BlockFragment: block.Bytes()[:cut], EndStream: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(wire.Bytes()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-fence.done:
			t.Fatal("END_STREAM released before complete header block")
		default:
		}
		wire.Reset()
		if err := framer.WriteContinuation(streamID, true, block.Bytes()[cut:]); err != nil {
			t.Fatal(err)
		}
		data := wire.Bytes()
		for i, b := range data {
			if _, err := c.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
			if i < len(data)-1 {
				select {
				case <-fence.done:
					t.Fatal("partial socket write released fence")
				default:
				}
			}
		}
		select {
		case <-fence.done:
		default:
			t.Fatal("fully written response retained fence")
		}
		c.expire(fence.token)
		if c.closed.Load() {
			t.Fatal("late deadline closed a completed connection")
		}
	}
}

func TestRoleDeliveryClosesOnMalformedOrFailedWrites(t *testing.T) {
	for _, partial := range []bool{false, true} {
		sink := &deliverySink{}
		if partial {
			sink.limit = 3
		}
		c := newRoleDeliveryConn(sink)
		fence, err := c.register()
		if err != nil {
			t.Fatal(err)
		}
		// SETTINGS frame cannot have a stream ID.
		if _, err := c.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 1}); err == nil {
			t.Fatal("invalid output accepted")
		}
		select {
		case <-fence.done:
		default:
			t.Fatal("connection failure retained fence")
		}
		if !sink.closed.Load() {
			t.Fatal("lease released before closing socket")
		}
		if _, err := c.Write([]byte("late data")); !errors.Is(err, net.ErrClosed) {
			t.Fatal("closed transport accepted more bytes")
		}
	}
}

type deliveryGate struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

type deliveryGateConn struct {
	net.Conn
	gate *deliveryGate
}

func (c *deliveryGateConn) Write(p []byte) (int, error) {
	if c.gate.armed.Load() {
		select {
		case c.gate.entered <- struct{}{}:
		default:
		}
		select {
		case <-c.gate.release:
		case <-c.gate.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *deliveryGateConn) Close() error {
	c.gate.once.Do(func() { close(c.gate.closed) })
	return c.Conn.Close()
}

type deliveryGateListener struct {
	net.Listener
	gate *deliveryGate
}

func (l *deliveryGateListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &deliveryGateConn{Conn: c, gate: l.gate}, nil
}

func startDeliveryGateServer(t *testing.T, b *deliveryReadBackend, gate *deliveryGate) (*Server, string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := zap.NewNop()
	srv := New(ln.Addr().String(), b, nil, logger, query.New("", logger, b))
	srv.listen = func(string, string) (net.Listener, error) {
		return &deliveryGateListener{Listener: ln, gate: gate}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := srv.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return srv, ln.Addr().String()
}

type deliveryReadBackend struct {
	*roleGRPCBackend
	mu            sync.RWMutex
	gate          *deliveryGate
	released      chan struct{}
	afterDelivery func()
}

func (b *deliveryReadBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	defer func() { b.released <- struct{}{} }()
	if b.gate != nil {
		b.gate.armed.Store(true)
	}
	err := deliver(ctx, &broadcast.TreeSnapshot{TotalChannels: 2, RootChannels: []*broadcast.ChannelNode{
		{ChannelID: 2, Name: "Visible", ChannelType: 2, MaxClients: 25, ClientCount: 1, Children: []*broadcast.ChannelNode{{ChannelID: 3, ParentID: 2, Name: "Child"}}},
	}})
	if b.afterDelivery != nil {
		b.afterDelivery()
	}
	return err
}

func TestRoleGRPCReadRetainsLeaseThroughSocketWrite(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "deadline"}[expire], func(t *testing.T) {
			gate := &deliveryGate{entered: make(chan struct{}, 1), release: make(chan struct{}), closed: make(chan struct{})}
			b := &deliveryReadBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
				return auth.IntegrationPrincipal{}, nil
			}}, gate: gate, released: make(chan struct{}, 4)}
			_, addr := startDeliveryGateServer(t, b, gate)
			conn := dialGRPC(t, addr)
			client := noxav1.NewControlClient(conn)
			ctx := roleAuthCtx(t, "integration", "pw")
			if expire {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			result := make(chan error, 1)
			go func() {
				response, err := client.ListChannels(ctx, &noxav1.ListChannelsRequest{})
				if err == nil && (len(response.Channels) != 2 || response.Channels[0].Name != "Visible") {
					err = errors.New("filtered channel fields lost")
				}
				result <- err
			}()
			select {
			case <-gate.entered:
			case err := <-result:
				t.Fatalf("response did not reach socket: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("socket write did not start")
			}
			revoked := make(chan struct{})
			go func() { b.mu.Lock(); defer b.mu.Unlock(); close(revoked) }()
			select {
			case <-revoked:
				t.Fatal("policy lease ended before socket write")
			case <-time.After(25 * time.Millisecond):
			}
			if !expire {
				close(gate.release)
			}
			select {
			case err := <-result:
				if (!expire && err != nil) || (expire && err == nil) {
					t.Fatalf("RPC result: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("RPC remained blocked")
			}
			select {
			case <-revoked:
			case <-time.After(3 * time.Second):
				t.Fatal("socket completion/closure retained policy lease")
			}
			if !expire {
				for _, root := range []string{"2", "9999"} {
					response, err := client.ListChannels(ctx, &noxav1.ListChannelsRequest{RootChannelId: root})
					if err != nil || (root == "2" && len(response.Channels) != 2) || (root == "9999" && len(response.Channels) != 0) {
						t.Fatalf("same-connection subtree: %v %v", response, err)
					}
				}
			}
		})
	}
}

func TestRoleGRPCShutdownWaitsForProtectedReadCallback(t *testing.T) {
	gate := &deliveryGate{entered: make(chan struct{}, 1), release: make(chan struct{}), closed: make(chan struct{})}
	close(gate.release)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	b := &deliveryReadBackend{
		roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
			return auth.IntegrationPrincipal{}, nil
		}},
		gate: gate, released: make(chan struct{}, 1),
		afterDelivery: func() { close(entered); <-release },
	}
	srv, addr := startDeliveryGateServer(t, b, gate)
	client := noxav1.NewControlClient(dialGRPC(t, addr))
	if _, err := client.ListChannels(roleAuthCtx(t, "integration", "pw"), &noxav1.ListChannelsRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not finish delivery")
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- srv.Shutdown(t.Context()) }()
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown outlived policy callback: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish after callback")
	}
}

func TestRoleGRPCReadDeadlineBoundsHTTP2FlowControl(t *testing.T) {
	b := &deliveryReadBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}, released: make(chan struct{}, 1)}
	addr := startGRPC(t, b, nil)
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}); err != nil {
		t.Fatal(err)
	}
	var headers bytes.Buffer
	encoder := hpack.NewEncoder(&headers)
	for _, field := range []hpack.HeaderField{
		{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "http"}, {Name: ":authority", Value: addr},
		{Name: ":path", Value: noxav1.Control_ListChannels_FullMethodName}, {Name: "content-type", Value: "application/grpc"},
		{Name: "te", Value: "trailers"}, {Name: "grpc-timeout", Value: "300m"},
		{Name: "noxa-authorization-model", Value: "roles-v1"},
		{Name: "authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("integration:pw"))},
	} {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: headers.Bytes(), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if err := framer.WriteData(1, true, []byte{0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				readDone <- nil
				return
			}
			if data, ok := frame.(*http2.DataFrame); ok && len(data.Data()) > 0 {
				readDone <- errors.New("server sent DATA without stream credit")
				return
			}
		}
	}()
	select {
	case <-b.released:
		t.Fatal("flow-controlled response released lease before deadline")
	case <-time.After(80 * time.Millisecond):
	}
	select {
	case <-b.released:
	case <-time.After(2 * time.Second):
		t.Fatal("flow-control stall retained lease beyond deadline")
	}
	_ = conn.Close()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
}

func TestRoleGRPCListChannelsRequiresProtectedTransport(t *testing.T) {
	_, err := protectedUnaryRead(t.Context(), nil, func(context.Context, func(*noxav1.ListChannelsResponse) error) error {
		t.Fatal("unprotected read reached backend")
		return nil
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
