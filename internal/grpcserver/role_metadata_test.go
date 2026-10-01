package grpcserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type metadataGRPCBackend struct {
	*roleGRPCBackend
	calls  atomic.Int32
	denied atomic.Bool
}

func (b *metadataGRPCBackend) check(ctx context.Context) error {
	b.calls.Add(1)
	if b.denied.Load() {
		return authorization.ErrRoleForbidden
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second {
		return errors.New("unbounded metadata read")
	}
	return nil
}

func (b *metadataGRPCBackend) WithIntegrationServerInfo(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerInfoResponse) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	return deliver(ctx, netproto.ServerInfoResponse{Name: "Private | server", Version: "test", Platform: "windows/amd64", UptimeSeconds: 45, ClientsOnline: 2, ChannelsOnline: 3, MaxClients: 25, MOTD: "Hello\n世界"})
}

func (b *metadataGRPCBackend) WithIntegrationClientInfo(ctx context.Context, _ auth.IntegrationPrincipal, target string, deliver func(context.Context, netproto.ClientInfoResponse) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	if target != "visible" {
		return authorization.ErrRoleForbidden
	}
	return deliver(ctx, netproto.ClientInfoResponse{ClientID: target, UniqueID: "canonical", Nickname: "Visible", ChannelID: 2, PingMs: -1})
}

func (b *metadataGRPCBackend) WithIntegrationServerConfig(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerConfig) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	return deliver(ctx, netproto.ServerConfig{MaxClients: 25, ClientTimeoutSeconds: 90, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true, OpusStereo: true})
}

func (b *metadataGRPCBackend) WithIntegrationBans(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.BanQuery, deliver func(context.Context, netproto.BanPage) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	if request.BeforeID != 100 || request.Limit != 2 {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.BanPage{NextBeforeID: 98, Bans: []netproto.BanEntry{
		{ID: 99, Type: 1, Value: "banned", Reason: "Reason | with\nnewlines", BannedBy: "actor", CreatedAt: 1700000000, ExpiresAt: 1700000600},
		{ID: 98, Type: 0, Value: "192.0.2.1", CreatedAt: 1700000000},
	}})
}

func TestRoleGRPCMetadataAndBanReads(t *testing.T) {
	b := &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	conn := dialGRPC(t, startGRPC(t, b, nil))
	ctx := roleAuthCtx(t, "integration", "pw")
	tests := []struct {
		method        string
		request, want proto.Message
	}{
		{noxav1.Control_GetServerInfo_FullMethodName, &noxav1.GetServerInfoRequest{}, &noxav1.GetServerInfoResponse{Name: "Private | server", Version: "test", Platform: "windows/amd64", UptimeSeconds: 45, ClientsOnline: 2, ChannelsOnline: 3, MaxClients: 25, Motd: "Hello\n世界"}},
		{noxav1.Control_GetClientInfo_FullMethodName, &noxav1.GetClientInfoRequest{ClientId: "visible"}, &noxav1.GetClientInfoResponse{ClientId: "visible", UniqueId: "canonical", Nickname: "Visible", ChannelId: 2, PingMs: -1}},
		{noxav1.Control_GetServerConfig_FullMethodName, &noxav1.GetServerConfigRequest{}, &noxav1.GetServerConfigResponse{MaxClients: 25, ClientTimeoutSeconds: 90, OpusBitrate: 64000, OpusFec: true, OpusDtx: true, OpusStereo: true}},
		{noxav1.Control_ListBans_FullMethodName, &noxav1.ListBansRequest{BeforeId: 100, Limit: 2}, &noxav1.ListBansResponse{NextBeforeId: 98, Bans: []*noxav1.BanRecord{
			{Id: 99, Type: 1, Value: "banned", Reason: "Reason | with\nnewlines", BannedBy: "actor", CreatedAt: 1700000000, ExpiresAt: 1700000600},
			{Id: 98, Type: 0, Value: "192.0.2.1", CreatedAt: 1700000000},
		}}},
	}
	legacy := dialGRPC(t, startGRPC(t, &stubBackend{}, nil))
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			response := tt.want.ProtoReflect().New().Interface()
			if err := conn.Invoke(ctx, tt.method, tt.request, response); err != nil || !proto.Equal(response, tt.want) {
				t.Fatalf("response: %v %v", response, err)
			}
			b.denied.Store(true)
			if err := conn.Invoke(ctx, tt.method, tt.request, response); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("revoked read: %v", err)
			}
			b.denied.Store(false)
			if err := conn.Invoke(roleModelCtx(t.Context()), tt.method, tt.request, response); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("missing credentials: %v", err)
			}
			if err := legacy.Invoke(roleAuthCtx(t, "admin-uid", "pw"), tt.method, tt.request, response); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("legacy read: %v", err)
			}
		})
	}
	client := noxav1.NewControlClient(conn)
	before := b.calls.Load()
	for _, request := range []*noxav1.ListBansRequest{{BeforeId: -1}, {Limit: -1}, {Limit: 101}} {
		if _, err := client.ListBans(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := client.GetClientInfo(ctx, &noxav1.GetClientInfoRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if b.calls.Load() != before {
		t.Fatal("invalid input reached backend")
	}
	if _, err := client.GetClientInfo(ctx, &noxav1.GetClientInfoRequest{ClientId: "hidden"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
}
