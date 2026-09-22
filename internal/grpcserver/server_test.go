package grpcserver

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/eventbus"
	"noxa/internal/metrics"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

// stubBackend implements query.Backend far enough for the RPCs served here.
// Everything else panics, which keeps an accidental new call site visible.
type stubBackend struct {
	query.Backend
	channels []query.ChannelInfo
}

func (*stubBackend) RoleIntegrationsEnabled() bool { return false }

type countingLoginLimiter struct{ calls int }

func (l *countingLoginLimiter) ReserveLoginAttempt(...string) (*auth.LoginAttempt, bool) {
	l.calls++
	panic("draining interceptor invoked login limiter")
}

// startGRPC starts a server on an ephemeral port and returns its address.
func startGRPC(t *testing.T, backend query.Backend, bus *eventbus.Bus) string {
	return startGRPCWith(t, backend, bus, nil)
}

func startGRPCWith(t *testing.T, backend query.Backend, bus *eventbus.Bus, mutate func(*Server)) string {
	t.Helper()
	srv, addr, cancel, errCh := startGRPCServer(t, backend, bus, mutate)
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			t.Errorf("grpc shutdown: %v", err)
		}
		if err := <-errCh; err != nil {
			t.Errorf("grpc start: %v", err)
		}
	})
	return addr
}

func startGRPCServer(t *testing.T, backend query.Backend, bus *eventbus.Bus, mutate func(*Server)) (*Server, string, context.CancelFunc, <-chan error) {
	t.Helper()
	srv, addr := newGRPCServer(t, backend, bus, mutate)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	waitForGRPCListener(t, addr)
	return srv, addr, cancel, errCh
}

func newGRPCServer(t *testing.T, backend query.Backend, bus *eventbus.Bus, mutate func(*Server)) (*Server, string) {
	return newGRPCServerWithLogger(t, backend, bus, zap.NewNop(), mutate)
}

func newGRPCServerWithLogger(t *testing.T, backend query.Backend, bus *eventbus.Bus, logger *zap.Logger, mutate func(*Server)) (*Server, string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	limiter := query.New("127.0.0.1:0", logger, backend)
	srv := New(addr, backend, bus, logger, limiter)
	if mutate != nil {
		mutate(srv)
	}
	return srv, addr
}

func waitForGRPCListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gRPC listener %s did not become ready", addr)
}

// dialGRPC opens a client connection.
func dialGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// authCtx returns a context carrying Basic credentials.
func authCtx(t *testing.T, user, password string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return metadata.AppendToOutgoingContext(ctx, "authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
}

// TestControlRPCs verifies the administration RPCs run against the query
// backend (232).
func TestSubtreeTraversesShuffledDescendants(t *testing.T) {
	all := []query.ChannelInfo{
		{ChannelID: 3, ParentID: 2, Name: "grandchild"},
		{ChannelID: 2, ParentID: 1, Name: "child"},
		{ChannelID: 1, Name: "root"},
		{ChannelID: 4, Name: "other"},
	}
	keep := subtree(all, 1)
	for _, id := range []int64{1, 2, 3} {
		if !keep[id] {
			t.Fatalf("subtree omitted reachable channel %d: %v", id, keep)
		}
	}
	if keep[4] {
		t.Fatalf("subtree included unrelated channel: %v", keep)
	}
	if got := subtree(all, 99); len(got) != 0 {
		t.Fatalf("nonexistent root = %v, want no channels", got)
	}
}

func TestSubtreeTraversalMatrixAndDuplicatePolicy(t *testing.T) {
	all := []query.ChannelInfo{
		{ChannelID: 4, ParentID: 3, Name: "grandchild"},
		{ChannelID: 2, ParentID: 1, Name: "child"},
		{ChannelID: 1, ParentID: 2, Name: "root-in-cycle"},
		{ChannelID: 3, ParentID: 2, Name: "child-before-parent"},
		{ChannelID: 2, ParentID: 99, Name: "duplicate-ignored"},
		{ChannelID: 9, Name: "unrelated"},
	}
	if got := subtree(all, 0); len(got) != 5 {
		t.Fatalf("root 0 keeps %d IDs, want 5 canonical IDs: %v", len(got), got)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if !subtree(all, 1)[id] {
			t.Fatalf("cyclic/shuffled subtree omitted %d", id)
		}
	}
	if subtree(all, 1)[9] {
		t.Fatalf("subtree included unrelated ID: %v", subtree(all, 1))
	}
	if got := subtree(all, 99); len(got) != 0 {
		t.Fatalf("duplicate row changed nonexistent-root semantics: %v", got)
	}
	canonical := uniqueChannels(all)
	if len(canonical) != 5 || canonical[1].Name != "child" {
		t.Fatalf("duplicate policy did not retain first occurrence: %+v", canonical)
	}
}

func TestListChannelsPreservesCanonicalInputOrder(t *testing.T) {
	backend := &stubBackend{channels: []query.ChannelInfo{
		{ChannelID: 4, ParentID: 3, Name: "grandchild"},
		{ChannelID: 2, ParentID: 1, Name: "child"},
		{ChannelID: 1, ParentID: 2, Name: "root-in-cycle"},
		{ChannelID: 3, ParentID: 2, Name: "child-before-parent"},
		{ChannelID: 2, ParentID: 99, Name: "duplicate-ignored"},
		{ChannelID: 9, Name: "unrelated"},
	}}
	service := &controlService{logger: zap.NewNop()}
	for _, test := range []struct {
		root string
		want []string
	}{
		{root: "", want: []string{"4", "2", "1", "3", "9"}},
		{root: "1", want: []string{"4", "2", "1", "3"}},
		{root: "99", want: nil},
	} {
		var root int64
		if test.root != "" {
			root, _ = strconv.ParseInt(test.root, 10, 64)
		}
		response := service.channelListResponse(backend.channels, root)
		got := make([]string, 0, len(response.GetChannels()))
		for _, channel := range response.GetChannels() {
			got = append(got, channel.GetId())
		}
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Fatalf("ListChannels(root=%q) IDs = %v, want %v", test.root, got, test.want)
		}
	}
}

func TestListChannelsSkipsInvalidCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		channel query.ChannelInfo
	}{
		{name: "negative max clients", channel: query.ChannelInfo{ChannelID: 1, MaxClients: -1}},
		{name: "negative current clients", channel: query.ChannelInfo{ChannelID: 1, ClientCount: -1}},
	}
	if strconv.IntSize == 64 {
		tooLarge := int(int64(math.MaxInt32) + 1)
		tests = append(tests,
			struct {
				name    string
				channel query.ChannelInfo
			}{name: "max clients overflow", channel: query.ChannelInfo{ChannelID: 1, MaxClients: tooLarge}},
			struct {
				name    string
				channel query.ChannelInfo
			}{name: "current clients overflow", channel: query.ChannelInfo{ChannelID: 1, ClientCount: tooLarge}},
		)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &controlService{logger: zap.NewNop()}
			response := service.channelListResponse([]query.ChannelInfo{test.channel}, 0)
			if len(response.GetChannels()) != 0 {
				t.Fatalf("channelListResponse() = %+v; want an empty response", response)
			}
		})
	}
}

func TestGRPCAddressMustBeLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:12338", "localhost:12338", "[::1]:12338"} {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("validateLoopbackAddr(%q) = %v", addr, err)
		}
	}
	for _, addr := range []string{":12338", "0.0.0.0:12338", "192.0.2.1:12338"} {
		if err := validateLoopbackAddr(addr); err == nil {
			t.Errorf("validateLoopbackAddr(%q) accepted public bind", addr)
		}
	}
}

func TestDrainingInterceptorsRejectBeforeAuthenticationAndHandlers(t *testing.T) {
	authCalls := 0
	backend := &stubBackend{}
	limiter := &countingLoginLimiter{}
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	srv := New("127.0.0.1:12338", backend, bus, zap.NewNop(), limiter)
	srv.draining.Store(true)
	handlerCalls := 0

	_, err := srv.unaryAuth(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpcserver.test/Ping"}, func(context.Context, any) (any, error) {
		handlerCalls++
		panic("draining interceptor invoked unary handler")
	})
	if status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "server shutting down" {
		t.Fatalf("draining unary = %v, want Unavailable", err)
	}
	err = srv.streamAuth(nil, nil, &grpc.StreamServerInfo{FullMethod: "/grpcserver.test/Ping"}, func(any, grpc.ServerStream) error {
		handlerCalls++
		panic("draining interceptor invoked stream handler")
	})
	if status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "server shutting down" {
		t.Fatalf("draining stream = %v, want Unavailable", err)
	}
	if authCalls != 0 || limiter.calls != 0 || handlerCalls != 0 {
		t.Fatalf("draining callbacks: auth=%d limiter=%d handler=%d, want zero", authCalls, limiter.calls, handlerCalls)
	}
}

func TestGRPCLoginFailuresArePrincipalScoped(t *testing.T) {
	backend := &roleGRPCBackend{authenticate: func(_ context.Context, id, password, _ string) (auth.IntegrationPrincipal, error) {
		if (id == "admin-uid" || id == "other-admin") && password == "pw" {
			return auth.IntegrationPrincipal{}, nil
		}
		return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
	}}
	limiter := query.New("127.0.0.1:0", zap.NewNop(), backend)
	limiter.MaxLoginFailures = 2
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	server := New("127.0.0.1:12338", backend, bus, zap.NewNop(), limiter)

	for range 2 {
		_, err := server.authenticateRoleIntegration(context.Background(), backend, "admin-uid", "wrong")
		if !errors.Is(err, auth.ErrIntegrationDenied) {
			t.Fatalf("failed login = %v", err)
		}
	}

	_, err := server.authenticateRoleIntegration(context.Background(), backend, "other-admin", "pw")
	if err != nil {
		t.Fatalf("other principal was locked out: %v", err)
	}
	_, err = server.authenticateRoleIntegration(context.Background(), backend, "admin-uid", "pw")
	if !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("locked principal was accepted: %v", err)
	}
}

func TestGRPCUnknownCredentialsMatchWrongPasswordAndAreMetered(t *testing.T) {
	backend := &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
	}}
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	control := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, backend, bus)))

	var firstMessage string
	for _, credentials := range [][2]string{{"admin-uid", "wrong"}, {"unknown-principal", "wrong"}} {
		_, err := control.ListChannels(roleAuthCtx(t, credentials[0], credentials[1]), &noxav1.ListChannelsRequest{})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("credentials %q: %v, want Unauthenticated", credentials[0], err)
		}
		if firstMessage == "" {
			firstMessage = status.Convert(err).Message()
		} else if got := status.Convert(err).Message(); got != firstMessage {
			t.Fatalf("wrong and unknown credential responses differ: %q != %q", got, firstMessage)
		}
	}

	m := metrics.New()
	limiter := query.New("127.0.0.1:0", zap.NewNop(), backend)
	limiter.SetMetrics(m)
	server := New("127.0.0.1:12338", backend, bus, zap.NewNop(), limiter)
	for _, principal := range []string{"admin-uid", "unknown-principal"} {
		_, err := server.authenticateRoleIntegration(context.Background(), backend, principal, "wrong")
		if !errors.Is(err, auth.ErrIntegrationDenied) {
			t.Fatalf("authenticateRoleIntegration(%q) = %v", principal, err)
		}
	}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "noxa_auth_failures_total" && len(family.GetMetric()) == 1 &&
			family.GetMetric()[0].GetCounter().GetValue() == 2 {
			return
		}
	}
	t.Fatal("gRPC unknown credentials did not emit the bounded invalid-credential metric")
}

func TestParseBasicRejectsOversizedMetadataBeforeDecoding(t *testing.T) {
	_, _, err := parseBasic("Basic " + strings.Repeat("A", 1024))
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("parseBasic(oversized) error = %v", err)
	}
}

func TestGRPCRejectsOversizedAndMultipleAuthorizationMetadata(t *testing.T) {
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	backend := &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}
	control := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, backend, bus)))

	valid := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin-uid:pw"))
	multiple := metadata.AppendToOutgoingContext(roleModelCtx(context.Background()),
		"authorization", valid,
		"authorization", valid,
	)
	if _, err := control.ListChannels(multiple, &noxav1.ListChannelsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("multiple authorization values = %v, want Unauthenticated", err)
	}

	overlong := metadata.AppendToOutgoingContext(roleModelCtx(context.Background()),
		"authorization", "Basic "+strings.Repeat("A", maxHeaderListBytes*2),
	)
	if _, err := control.ListChannels(overlong, &noxav1.ListChannelsRequest{}); err == nil {
		t.Fatal("oversized authorization metadata was accepted")
	}
}

func TestGRPCTransportLimitsRejectOversizedRequestsAndMetadataBeforeBackend(t *testing.T) {
	backend := &stubBackend{}
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	control := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, backend, bus)))

	_, err := control.Authenticate(context.Background(), &noxav1.AuthenticateRequest{
		Username: strings.Repeat("x", maxReceiveMessageBytes),
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request = %v, want ResourceExhausted", err)
	}

	_, err = control.Authenticate(context.Background(), &noxav1.AuthenticateRequest{
		Username: "small",
		Password: strings.Repeat("x", maxReceiveMessageBytes),
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request password = %v, want ResourceExhausted", err)
	}
}

func TestGRPCListChannelsCancellationPropagatesToBackend(t *testing.T) {
	for _, tc := range []struct {
		name     string
		newCtx   func() (context.Context, context.CancelFunc)
		wantCode codes.Code
		deadline bool
	}{
		{
			name: "cancel",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantCode: codes.Canceled,
		},
		{
			name: "deadline",
			newCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 250*time.Millisecond)
			},
			wantCode: codes.DeadlineExceeded,
			deadline: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{})
			type observation struct {
				err            error
				deadline       time.Time
				hasDeadline    bool
				expiredAtEntry bool
			}
			seen := make(chan observation, 1)
			backend := &blockingSnapshotBackend{eventTestBackend: &eventTestBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
				return auth.IntegrationPrincipal{}, nil
			}}}, read: func(ctx context.Context) error {
				deadline, hasDeadline := ctx.Deadline()
				expiredAtEntry := hasDeadline && !time.Now().Before(deadline)
				close(entered)
				<-ctx.Done()
				seen <- observation{
					err:            ctx.Err(),
					deadline:       deadline,
					hasDeadline:    hasDeadline,
					expiredAtEntry: expiredAtEntry,
				}
				return ctx.Err()
			}}
			bus := eventbus.New(zap.NewNop())
			defer bus.Close()
			control := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, backend, bus)))

			ctx, cancel := tc.newCtx()
			defer cancel()
			callerDeadline, callerHasDeadline := ctx.Deadline()
			ctx = roleModelCtx(withAuth(ctx))
			done := make(chan error, 1)
			go func() {
				_, err := control.ListChannels(ctx, &noxav1.ListChannelsRequest{})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("backend was not entered")
			}
			if !tc.deadline {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("RPC did not finish after context completion")
			}
			if status.Code(err) != tc.wantCode {
				t.Fatalf("ListChannels = %v, want %v", err, tc.wantCode)
			}
			var observed observation
			select {
			case observed = <-seen:
			case <-time.After(time.Second):
				t.Fatal("backend did not observe context completion")
			}
			if !tc.deadline {
				if !errors.Is(observed.err, context.Canceled) {
					t.Fatalf("backend context = %v, want Canceled", observed.err)
				}
				return
			}
			if !callerHasDeadline || !observed.hasDeadline || observed.expiredAtEntry {
				t.Fatalf("backend deadline propagation = caller:%v backend:%v expired:%t", callerHasDeadline, observed.hasDeadline, observed.expiredAtEntry)
			}
			if delta := observed.deadline.Sub(callerDeadline); delta < -50*time.Millisecond || delta > 50*time.Millisecond {
				t.Fatalf("backend deadline drift = %v, want within 50ms", delta)
			}
			if !errors.Is(observed.err, context.Canceled) && !errors.Is(observed.err, context.DeadlineExceeded) {
				t.Fatalf("backend deadline completion = %v, want Canceled or DeadlineExceeded", observed.err)
			}
		})
	}
}

// TestUnauthenticatedRPCsAreRefused verifies the metadata credential gate
// (232).
func TestUnauthenticatedRPCsAreRefused(t *testing.T) {
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	backend := &eventTestBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(_ context.Context, id, password, _ string) (auth.IntegrationPrincipal, error) {
		if id == "boom" {
			return auth.IntegrationPrincipal{}, errors.New("backend down")
		}
		if id != "admin-uid" || password != "pw" {
			return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
		}
		return auth.IntegrationPrincipal{}, nil
	}}}
	conn := dialGRPC(t, startGRPC(t, backend, bus))
	control := noxav1.NewControlClient(conn)

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"no metadata", roleModelCtx(context.Background()), codes.Unauthenticated},
		{"wrong password", roleAuthCtx(t, "admin-uid", "nope"), codes.Unauthenticated},
		{"not an integration", roleAuthCtx(t, "user-uid", "pw"), codes.Unauthenticated},
		{"backend error", roleAuthCtx(t, "boom", "pw"), codes.Unavailable},
	} {
		_, err := control.ListChannels(tc.ctx, &noxav1.ListChannelsRequest{})
		if status.Code(err) != tc.want {
			t.Fatalf("%s: code = %v, want %v", tc.name, status.Code(err), tc.want)
		}
	}

	// Streams are gated by the same rule.
	stream, err := noxav1.NewEventsClient(conn).Subscribe(roleModelCtx(context.Background()), &noxav1.SubscribeEventsRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated Subscribe = %v", status.Code(err))
	}
}

// TestFileTransferRPCsAreUnimplemented pins the deliberate refusal to mint
// transfer tokens from the bot API (232).
func TestFileTransferRPCsAreUnimplemented(t *testing.T) {
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, bus)))
	_, err := client.StartFileTransfer(authCtx(t, "admin-uid", "pw"), &noxav1.StartFileTransferRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("StartFileTransfer = %v", status.Code(err))
	}
}
