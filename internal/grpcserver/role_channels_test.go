package grpcserver

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

type roleChannelGRPCBackend struct {
	*roleGRPCBackend
	query.RoleChannelBackend
	changeChannel func(context.Context, auth.IntegrationPrincipal, netproto.RoleChannelChange) (netproto.RoleChannelResult, error)
}

func (b *roleChannelGRPCBackend) ChangeIntegrationChannel(ctx context.Context, p auth.IntegrationPrincipal, change netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	return b.changeChannel(ctx, p, change)
}

func TestRoleGRPCChannelLifecycle(t *testing.T) {
	var changes atomic.Int32
	want := netproto.RoleChannelChange{
		Kind: authorization.ChannelCreate, ExpectedRevision: 7, ParentID: 2, ChannelType: 2, Password: "channel secret",
		Settings: &netproto.RoleChannelSettings{Name: "Room", Topic: "Topic", Description: "Description", OrderIndex: 4, MaxClients: 5, SlowModeSeconds: 6, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true, OpusStereo: true},
		Access:   &netproto.RoleChannelAccess{Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Allow}}},
	}
	b := &roleChannelGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	b.changeChannel = func(_ context.Context, _ auth.IntegrationPrincipal, got netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
		changes.Add(1)
		if !reflect.DeepEqual(got, want) {
			return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
		}
		return netproto.RoleChannelResult{Revision: 8, ChannelID: 12, EnforcementPending: true}, nil
	}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	req := &noxav1.ChangeChannelRequest{Kind: "channel_create", ExpectedRevision: 7, ParentId: 2, ChannelType: 2, Password: "channel secret",
		Settings: &noxav1.RoleChannelSettings{Name: "Room", Topic: "Topic", Description: "Description", OrderIndex: 4, MaxClients: 5, SlowModeSeconds: 6, OpusBitrate: 64000, OpusFec: true, OpusDtx: true, OpusStereo: true},
		Access:   &noxav1.RoleChannelCreationAccess{Overrides: []*noxav1.ChannelRoleOverride{{RoleId: 10, Capability: "view_channel", Effect: "allow"}}},
	}
	result, err := c.ChangeChannel(roleAuthCtx(t, "integration", "pw"), req)
	if err != nil || result.GetRevision() != 8 || result.GetChannelId() != 12 || !result.GetEnforcementPending() {
		t.Fatalf("commit: %v %v", result, err)
	}
	req.ExpectedRevision = 0
	if _, err := c.ChangeChannel(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if changes.Load() != 1 {
		t.Fatal("missing revision reached lifecycle")
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ChangeChannel(roleAuthCtx(t, "admin-uid", "pw"), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	// Absent settings/access must stay absent on move/delete, not become
	// explicit zero-value objects which the native lifecycle correctly rejects.
	for _, kind := range []string{"channel_move", "channel_delete"} {
		got := channelChangeFromProto(&noxav1.ChangeChannelRequest{Kind: kind, ExpectedRevision: 9, ChannelId: 12, ParentId: 3, SyncToParent: true})
		if got.Settings != nil || got.Access != nil || got.ChannelID != 12 || got.ParentID != 3 || !got.SyncToParent {
			t.Fatalf("move/delete conversion: %+v", got)
		}
	}
}

func TestRoleGRPCChannelMovePreservesOptionalOrder(t *testing.T) {
	for _, order := range []*int32{nil, new(int32), func() *int32 { n := int32(-7); return &n }()} {
		b := &roleChannelGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
			return auth.IntegrationPrincipal{}, nil
		}}}
		b.changeChannel = func(_ context.Context, _ auth.IntegrationPrincipal, got netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
			if !reflect.DeepEqual(got.OrderIndex, order) || got.Kind != authorization.ChannelMove || got.ParentID != 3 || got.Settings != nil {
				return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
			}
			return netproto.RoleChannelResult{Revision: 8, ChannelID: 12}, nil
		}
		c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
		if _, err := c.ChangeChannel(roleAuthCtx(t, "integration", "pw"), &noxav1.ChangeChannelRequest{Kind: "channel_move", ExpectedRevision: 7, ChannelId: 12, ParentId: 3, OrderIndex: order}); err != nil {
			t.Fatal(err)
		}
	}
}
