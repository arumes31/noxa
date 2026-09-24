package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type rulesGRPCBackend struct {
	*roleGRPCBackend
	denied atomic.Bool
}

func (b *rulesGRPCBackend) WithIntegrationRules(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.RulesInspection) error) error {
	if b.denied.Load() {
		return auth.ErrIntegrationDenied
	}
	return deliver(ctx, netproto.RulesInspection{Text: "Rules | with\nnewlines", Hash: "wording-hash", AcceptedClients: 17})
}

func TestRoleGRPCRules(t *testing.T) {
	b := &rulesGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	result, err := c.GetServerRules(roleAuthCtx(t, "integration", "pw"), &noxav1.GetServerRulesRequest{})
	if err != nil || result.GetText() != "Rules | with\nnewlines" || result.GetHash() != "wording-hash" || result.GetAcceptedClients() != 17 {
		t.Fatalf("rules response: %v %v", result, err)
	}
	b.denied.Store(true)
	if _, err := c.GetServerRules(roleAuthCtx(t, "integration", "pw"), &noxav1.GetServerRulesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.GetServerRules(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.GetServerRulesRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
