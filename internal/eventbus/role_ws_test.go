package eventbus

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/state"
	noxav1 "noxa/v1"
)

type roleWSBackend struct {
	logins       atomic.Int32
	reads        atomic.Int32
	authenticate func(context.Context, string, string, string) (auth.IntegrationPrincipal, error)
	snapshot     func(context.Context, func(context.Context, *broadcast.TreeSnapshot) error) error
}

func (*roleWSBackend) RoleIntegrationsEnabled() bool { return true }
func (b *roleWSBackend) AuthenticateIntegration(ctx context.Context, user, password, remote string) (auth.IntegrationPrincipal, error) {
	b.logins.Add(1)
	if b.authenticate != nil {
		return b.authenticate(ctx, user, password, remote)
	}
	return auth.IntegrationPrincipal{}, nil
}
func (b *roleWSBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.reads.Add(1)
	if b.snapshot != nil {
		return b.snapshot(ctx, deliver)
	}
	return deliver(ctx, &broadcast.TreeSnapshot{})
}
func (b *roleWSBackend) WithIntegrationEvent(ctx context.Context, p auth.IntegrationPrincipal, _ Event, deliver func(context.Context, broadcast.IntegrationEvent) error) error {
	return b.WithIntegrationSnapshot(ctx, p, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
		return deliver(ctx, broadcast.IntegrationEvent{Snapshot: snapshot})
	})
}

func TestRoleWSUnavailableBackendDoesNotOpenLegacyStream(t *testing.T) {
	bus := New(zap.NewNop())
	t.Cleanup(bus.Close)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/events", nil)
	request.SetBasicAuth("admin", "password")
	recorder := httptest.NewRecorder()
	HandlerWithRoleBackend(bus, nil, zap.NewNop(), nil, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || bus.Stats().Subscribers != 0 {
		t.Fatalf("unavailable role backend opened event stream: status=%d subscribers=%d", recorder.Code, bus.Stats().Subscribers)
	}
}

func TestRoleWSRejectsIncompatibleModelsAndFiltersBeforeLogin(t *testing.T) {
	b := &roleWSBackend{}
	bus := New(zap.NewNop())
	t.Cleanup(bus.Close)
	handler := HandlerWithRoleBackend(bus, b, zap.NewNop(), nil, nil)
	for _, tc := range []struct {
		name   string
		models []string
		query  string
		want   int
	}{
		{"missing", nil, "", http.StatusPreconditionFailed},
		{"legacy", []string{"legacy"}, "", http.StatusPreconditionFailed},
		{"duplicate", []string{"roles-v1", "roles-v1"}, "", http.StatusPreconditionFailed},
		{"raw event", []string{"roles-v1"}, "?types=user_moved", http.StatusBadRequest},
		{"speaking only", []string{"roles-v1"}, "?types=speaking_changed", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/events"+tc.query, nil)
			r.SetBasicAuth("reader", "password")
			for _, model := range tc.models {
				r.Header.Add("Noxa-Authorization-Model", model)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || w.Header().Get("Noxa-Authorization-Model") != "roles-v1" || b.logins.Load() != 0 || b.reads.Load() != 0 {
				t.Fatalf("unexpected rejection: status=%d header=%v logins=%d reads=%d", w.Code, w.Header(), b.logins.Load(), b.reads.Load())
			}
		})
	}
}

func dialRoleWS(t *testing.T, endpoint string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(endpoint, "http"), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("reader:pw")))
	cfg.Header.Set("Noxa-Authorization-Model", "roles-v1")
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readRoleWSEvent(t *testing.T, conn *websocket.Conn) *noxav1.Event {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := websocket.Message.Receive(conn, &data); err != nil {
		t.Fatal(err)
	}
	message := &noxav1.Event{}
	if err := protojson.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	return message
}

func waitForSubscribers(t *testing.T, bus *Bus, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if bus.Stats().Subscribers == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber count = %d, want %d", bus.Stats().Subscribers, n)
}

func TestRoleWSIdleLifetimeAndCapacityRelease(t *testing.T) {
	bus := New(zap.NewNop())
	t.Cleanup(bus.Close)
	cfg := testWSHandlerConfig(time.Now)
	cfg.maxConnections, cfg.roleBackend, cfg.streamLifetime = 1, &roleWSBackend{}, 500*time.Millisecond
	completed := make(chan struct{}, 4)
	handler := newWSHandler(bus, zap.NewNop(), cfg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { completed <- struct{}{} }()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	first := dialRoleWS(t, srv.URL)
	if message := readRoleWSEvent(t, first); message.GetRoleSnapshot() == nil {
		t.Fatalf("missing initial snapshot: %v", message)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	r.SetBasicAuth("reader", "pw")
	r.Header.Set("Noxa-Authorization-Model", "roles-v1")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	// The same handler owns its capacity; route through the running server.
	r.RequestURI = ""
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("capacity bypass: %d", response.StatusCode)
	}
	var data []byte
	err = websocket.Message.Receive(first, &data)
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("idle stream outlived limit: %v", err)
	}
	waitForSubscribers(t, bus, 0)
	for range 2 {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("stream/HTTP cleanup did not finish")
		}
	}
	third := dialRoleWS(t, srv.URL)
	if message := readRoleWSEvent(t, third); message.GetRoleSnapshot() == nil {
		t.Fatal("expired stream retained slot")
	}
}

type roleWSGateConn struct {
	net.Conn
	block     atomic.Bool
	entered   chan struct{}
	closed    chan struct{}
	finish    chan struct{}
	exited    chan struct{}
	closeOnce sync.Once
	deadline  atomic.Int64
}

func (c *roleWSGateConn) SetDeadline(at time.Time) error {
	if !at.IsZero() {
		c.deadline.CompareAndSwap(0, at.UnixNano())
	}
	return c.Conn.SetDeadline(at)
}
func (c *roleWSGateConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if !c.block.Load() {
		return c.Conn.Write(p)
	}
	close(c.entered)
	<-c.closed
	<-c.finish
	close(c.exited)
	return 0, net.ErrClosed
}
func (c *roleWSGateConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type roleWSGateListener struct {
	net.Listener
	accepted chan *roleWSGateConn
}

func (l *roleWSGateListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	gate := &roleWSGateConn{Conn: conn, entered: make(chan struct{}), closed: make(chan struct{}), finish: make(chan struct{}), exited: make(chan struct{})}
	l.accepted <- gate
	return gate, nil
}

func TestRoleWSRetainsLeaseUntilCanceledSocketWriteJoins(t *testing.T) {
	var policy sync.RWMutex
	var changed atomic.Bool
	leaseCtx, cancelLease := context.WithCancel(t.Context())
	defer cancelLease()
	b := &roleWSBackend{snapshot: func(_ context.Context, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
		policy.RLock()
		defer policy.RUnlock()
		name := "before"
		if changed.Load() {
			name = "after"
		}
		return deliver(leaseCtx, &broadcast.TreeSnapshot{RootChannels: []*broadcast.ChannelNode{{Channel: state.Channel{ChannelID: 1, Name: name}}}})
	}}
	bus := New(zap.NewNop())
	t.Cleanup(bus.Close)
	srv := httptest.NewUnstartedServer(HandlerWithRoleBackend(bus, b, zap.NewNop(), nil, nil))
	listener := &roleWSGateListener{Listener: srv.Listener, accepted: make(chan *roleWSGateConn, 1)}
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	conn := dialRoleWS(t, srv.URL)
	gate := <-listener.accepted
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(gate.finish) }) }
	t.Cleanup(func() { finish(); _ = gate.Close() })
	readRoleWSEvent(t, conn)
	if bound := time.Unix(0, gate.deadline.Load()); bound.Before(time.Now()) || bound.After(time.Now().Add(wsWriteTimeout)) {
		t.Fatalf("upgrade had no bounded socket deadline: %v", bound)
	}
	gate.block.Store(true)
	changed.Store(true)
	bus.Publish("channel_updated", []byte(`{}`))
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("protected write did not reach socket")
	}
	revoked := make(chan struct{})
	go func() { policy.Lock(); defer policy.Unlock(); close(revoked) }()
	select {
	case <-revoked:
		t.Fatal("lease released before socket completion")
	case <-time.After(20 * time.Millisecond):
	}
	cancelLease()
	select {
	case <-gate.closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close owned socket")
	}
	select {
	case <-revoked:
		t.Fatal("lease released before interrupted write joined")
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("socket closure retained lease")
	}
	select {
	case <-gate.exited:
	default:
		t.Fatal("revocation preceded socket completion")
	}
	waitForSubscribers(t, bus, 0)
}

func TestRoleWSDroppedEventClosesForResync(t *testing.T) {
	bus := New(zap.NewNop())
	bus.Buffer, bus.MaxDrops = 1, 100
	t.Cleanup(bus.Close)
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	b := &roleWSBackend{snapshot: func(ctx context.Context, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
		close(ready)
		<-release
		return deliver(ctx, &broadcast.TreeSnapshot{})
	}}
	srv := httptest.NewServer(HandlerWithRoleBackend(bus, b, zap.NewNop(), nil, nil))
	t.Cleanup(srv.Close)
	conn := dialRoleWS(t, srv.URL)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("initial snapshot did not start")
	}
	bus.Publish("channel_updated", []byte(`{}`))
	bus.Publish("channel_updated", []byte(`{}`))
	if bus.Stats().Dropped != 1 {
		t.Fatal("fixture did not drop an event")
	}
	unblock()
	readRoleWSEvent(t, conn)
	var data []byte
	err := websocket.Message.Receive(conn, &data)
	var timeout net.Error
	if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("dropped event kept stream open: %v", err)
	}
	waitForSubscribers(t, bus, 0)
}
