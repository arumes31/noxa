package grpcserver

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

func (b *metadataGRPCBackend) SetIntegrationServerConfig(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.ServerConfig) (netproto.ServerConfig, error) {
	if err := b.check(ctx); err != nil {
		return netproto.ServerConfig{}, err
	}
	return request, nil
}

func TestRoleGRPCConfigMutation(t *testing.T) {
	b := &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	request := &noxav1.SetServerConfigRequest{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000, OpusFec: true, OpusDtx: true, OpusStereo: true}
	for _, clear := range []bool{false, true} {
		if clear {
			request.MaxClients, request.OpusFec, request.OpusDtx, request.OpusStereo = 0, false, false, false
		}
		got, err := c.SetServerConfig(ctx(), request)
		if err != nil || got.GetMaxClients() != int64(request.MaxClients) || got.GetClientTimeoutSeconds() != 120 || got.GetOpusBitrate() != 64000 || got.GetOpusFec() != !clear || got.GetOpusDtx() != !clear || got.GetOpusStereo() != !clear {
			t.Fatalf("save acknowledgement: %v %v", got, err)
		}
	}
	for _, invalid := range []*noxav1.SetServerConfigRequest{
		{}, {MaxClients: -1, ClientTimeoutSeconds: 120, OpusBitrate: 64000}, {MaxClients: 100001, ClientTimeoutSeconds: 120, OpusBitrate: 64000},
		{ClientTimeoutSeconds: 29, OpusBitrate: 64000}, {ClientTimeoutSeconds: 86401, OpusBitrate: 64000},
		{ClientTimeoutSeconds: 120, OpusBitrate: 5999}, {ClientTimeoutSeconds: 120, OpusBitrate: 510001},
	} {
		if _, err := c.SetServerConfig(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 2 {
		t.Fatal("invalid save reached backend or mutation performed a refresh")
	}
	b.denied.Store(true)
	if _, err := c.SetServerConfig(ctx(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := c.SetServerConfig(roleModelCtx(t.Context()), request); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.SetServerConfig(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
