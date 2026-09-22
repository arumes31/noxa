package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/eventbus"
	noxav1 "noxa/v1"
)

type eventTestBackend struct {
	*roleGRPCBackend
	reads atomic.Int32
}

func (b *eventTestBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.reads.Add(1)
	return deliver(ctx, &broadcast.TreeSnapshot{})
}

func (*eventTestBackend) WithIntegrationEvent(context.Context, auth.IntegrationPrincipal, eventbus.Event, func(context.Context, broadcast.IntegrationEvent) error) error {
	return nil
}

func TestRoleEventNegotiationAndFiltersFailBeforeReads(t *testing.T) {
	var logins atomic.Int32
	b := &eventTestBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		logins.Add(1)
		return auth.IntegrationPrincipal{}, nil
	}}}
	bus := eventbus.New(zap.NewNop())
	t.Cleanup(bus.Close)
	client := noxav1.NewEventsClient(dialGRPC(t, startGRPC(t, b, bus)))
	for _, tc := range []struct {
		name   string
		models []string
		types  []noxav1.EventType
		code   codes.Code
	}{
		{"missing model", nil, nil, codes.FailedPrecondition},
		{"wrong model", []string{"legacy"}, nil, codes.FailedPrecondition},
		{"duplicate model", []string{"roles-v1", "roles-v1"}, nil, codes.FailedPrecondition},
		{"raw transition filter", []string{"roles-v1"}, []noxav1.EventType{noxav1.EventType_EVENT_TYPE_USER_MOVED}, codes.InvalidArgument},
		{"speaking without resync", []string{"roles-v1"}, []noxav1.EventType{noxav1.EventType_EVENT_TYPE_USER_SPEAKING}, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(authCtx(t, "integration", "pw"), time.Second)
			defer cancel()
			for _, model := range tc.models {
				ctx = metadata.AppendToOutgoingContext(ctx, "noxa-authorization-model", model)
			}
			before := logins.Load()
			stream, err := client.Subscribe(ctx, &noxav1.SubscribeEventsRequest{EventTypes: tc.types})
			if err == nil {
				_, err = stream.Recv()
			}
			if status.Code(err) != tc.code || b.reads.Load() != 0 {
				t.Fatalf("unsafe subscription: reads=%d error=%v", b.reads.Load(), err)
			}
			if tc.code == codes.FailedPrecondition && logins.Load() != before {
				t.Fatal("incompatible model reached credential verification")
			}
		})
	}
}

func TestRoleEventSubscriptionCapacityAndIdleLifetime(t *testing.T) {
	b := &eventTestBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	bus := eventbus.New(zap.NewNop())
	t.Cleanup(bus.Close)
	addr := startGRPCWith(t, b, bus, func(s *Server) { s.roleStreamSlots = make(chan struct{}, 1); s.StreamLifetime = 300 * time.Millisecond })
	client := noxav1.NewEventsClient(dialGRPC(t, addr))
	ctx := roleAuthCtx(t, "integration", "pw")
	first, err := client.Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Recv(); err != nil {
		t.Fatal(err)
	}
	second, err := client.Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
	if err == nil {
		_, err = second.Recv()
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("subscription limit ignored: %v", err)
	}
	if _, err := first.Recv(); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("idle stream outlived bound: %v", err)
	}
	third, err := client.Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.Recv(); err != nil {
		t.Fatalf("expired stream retained slot: %v", err)
	}
}
