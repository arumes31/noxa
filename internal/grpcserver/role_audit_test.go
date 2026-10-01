package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type auditGRPCBackend struct {
	*roleGRPCBackend
	calls  atomic.Int32
	denied atomic.Bool
}

func (b *auditGRPCBackend) WithIntegrationAudit(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.AuditLog, deliver func(context.Context, netproto.AuditLogResponse) error) error {
	b.calls.Add(1)
	if b.denied.Load() {
		return auth.ErrIntegrationDenied
	}
	if request.BeforeID != 20 || request.Limit != 2 {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.AuditLogResponse{Entries: []netproto.AuditEntry{
		{ID: 19, Actor: "actor", Action: "role_update", Target: "role:10", Detail: "Detail | with\nnewlines", CreatedAt: 100, Structured: true},
		{ID: 18, CreatedAt: 99, Restricted: true},
	}, Capabilities: []authorization.CapabilityInfo{{Key: authorization.Speak, Group: "voice", English: "Speak", German: "Sprechen", Channel: true, Requires: []authorization.Capability{authorization.Connect}}}})
}

func TestRoleGRPCAuditRead(t *testing.T) {
	b := &auditGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := roleAuthCtx(t, "integration", "pw")
	request := &noxav1.ListAuditLogRequest{BeforeId: 20, Limit: 2}
	page, err := c.ListAuditLog(ctx, request)
	want := &noxav1.ListAuditLogResponse{Entries: []*noxav1.AuditLogRecord{
		{Id: 19, Actor: "actor", Action: "role_update", Target: "role:10", Detail: "Detail | with\nnewlines", CreatedAt: 100, Structured: true},
		{Id: 18, CreatedAt: 99, Restricted: true},
	}, Capabilities: []*noxav1.CapabilityDescriptor{{Key: "speak", Group: "voice", English: "Speak", German: "Sprechen", Channel: true, Requires: []string{"connect"}}}}
	if err != nil || !proto.Equal(page, want) {
		t.Fatalf("audit response: %v %v", page, err)
	}
	for _, invalid := range []*noxav1.ListAuditLogRequest{{BeforeId: -1}, {Limit: -1}, {Limit: 201}} {
		if _, err := c.ListAuditLog(ctx, invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 1 {
		t.Fatal("invalid request reached backend")
	}
	b.denied.Store(true)
	if _, err := c.ListAuditLog(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ListAuditLog(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
