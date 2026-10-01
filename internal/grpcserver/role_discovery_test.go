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
	"noxa/internal/broadcast"
	"noxa/internal/state"
	noxav1 "noxa/v1"
)

type discoveryGRPCBackend struct {
	*roleGRPCBackend
	denied atomic.Bool
	calls  atomic.Int32
}

func (b *discoveryGRPCBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	b.calls.Add(1)
	if b.denied.Load() {
		return authorization.ErrRoleForbidden
	}
	return deliver(ctx, &broadcast.TreeSnapshot{TotalChannels: 2, TotalClients: 3,
		UnassignedClients: []*broadcast.ClientInfo{{ClientID: "unassigned", UniqueID: "uid-zero", Nickname: "No channel"}},
		RootChannels: []*broadcast.ChannelNode{{Channel: state.Channel{ChannelID: 2, Name: "Root"},
			Clients: []*broadcast.ClientInfo{{ClientID: "root", UniqueID: "uid-root", Nickname: "Root member", ChannelID: 2}},
			Children: []*broadcast.ChannelNode{{Channel: state.Channel{ChannelID: 3, ParentID: 2, Name: "Child", Topic: "Topic | with\nnewlines", ChannelType: 1, MaxClients: 25, ClientCount: 1, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true, OpusStereo: true, SlowModeSeconds: 2},
				Clients: []*broadcast.ClientInfo{{ClientID: "child", UniqueID: "uid-child", Nickname: "Child member", ChannelID: 3}},
			}},
		}},
	})
}

func TestRoleGRPCDiscoveryProjection(t *testing.T) {
	b := &discoveryGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := roleAuthCtx(t, "integration", "pw")
	clients, err := client.ListClients(ctx, &noxav1.ListClientsRequest{})
	wantClients := &noxav1.ListClientsResponse{Clients: []*noxav1.VisibleClient{
		{ClientId: "unassigned", UniqueId: "uid-zero", Nickname: "No channel"},
		{ClientId: "root", UniqueId: "uid-root", Nickname: "Root member", ChannelId: 2},
		{ClientId: "child", UniqueId: "uid-child", Nickname: "Child member", ChannelId: 3},
	}}
	if err != nil || !proto.Equal(clients, wantClients) {
		t.Fatalf("visible sessions: %v %v", clients, err)
	}
	channel, err := client.GetChannelInfo(ctx, &noxav1.GetChannelInfoRequest{ChannelId: 3})
	wantChannel := &noxav1.GetChannelInfoResponse{ChannelId: 3, ParentId: 2, Name: "Child", Topic: "Topic | with\nnewlines", ChannelType: 1, MaxClients: 25, CurrentClients: 1, OpusBitrate: 64000, OpusFec: true, OpusDtx: true, OpusStereo: true, SlowModeSeconds: 2}
	if err != nil || !proto.Equal(channel, wantChannel) {
		t.Fatalf("channel detail: %v %v", channel, err)
	}
	before := b.calls.Load()
	for _, id := range []int64{0, -1} {
		if _, err := client.GetChannelInfo(ctx, &noxav1.GetChannelInfoRequest{ChannelId: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if before != b.calls.Load() {
		t.Fatal("invalid ID reached backend")
	}
	if _, err := client.GetChannelInfo(ctx, &noxav1.GetChannelInfoRequest{ChannelId: 9999}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	b.denied.Store(true)
	if _, err := client.ListClients(ctx, &noxav1.ListClientsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := client.GetChannelInfo(ctx, &noxav1.GetChannelInfoRequest{ChannelId: 3}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ListClients(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.ListClientsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := legacy.GetChannelInfo(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.GetChannelInfoRequest{ChannelId: 3}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
