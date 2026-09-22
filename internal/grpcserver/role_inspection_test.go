package grpcserver

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

type inspectionGRPCBackend struct {
	*roleGRPCBackend
	query.RoleChannelBackend
	calls  atomic.Int32
	denied atomic.Bool
}

func (b *inspectionGRPCBackend) check(revision int64) error {
	b.calls.Add(1)
	if b.denied.Load() {
		return authorization.ErrRoleForbidden
	}
	if revision != 7 {
		return authorization.ErrRoleConflict
	}
	return nil
}

func (b *inspectionGRPCBackend) WithIntegrationRoleState(ctx context.Context, _ auth.IntegrationPrincipal, channelID int64, deliver func(context.Context, netproto.RoleState) error) error {
	if err := b.check(7); err != nil {
		return err
	}
	if channelID != 2 {
		return authorization.ErrRoleInvalid
	}
	o := authorization.RoleOverride{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Allow}
	return deliver(ctx, netproto.RoleState{
		ActorID: 1, ParentAccessAvailable: true, EffectiveOverrides: []authorization.RoleOverride{o}, ParentOverrides: []authorization.RoleOverride{o},
		ManageableRoleIDs: []int64{10}, GrantableCapabilities: []authorization.Capability{authorization.ViewChannel},
		Capabilities: []authorization.CapabilityInfo{{Key: authorization.SendMessages, Group: "text", English: "Send messages", German: "Nachrichten senden", Channel: true, Requires: []authorization.Capability{authorization.ViewChannel}}},
		Policy: authorization.RolePolicy{Revision: 7, OwnerID: 1, EveryoneID: 10, DefaultMemberRoleID: 11,
			Roles:    []authorization.Role{{ID: 11, Name: "Member", Position: 1, Color: "#123456", Icon: "M", Hoist: true, Permissions: []authorization.Capability{authorization.ViewChannel}}},
			Members:  []authorization.RoleMember{{UserID: 3, RoleIDs: []int64{11}}},
			Channels: []authorization.ChannelPolicy{{ChannelID: 2, ParentID: 1, Synced: true}},
		},
	})
}

func (b *inspectionGRPCBackend) WithIntegrationRoleMembers(ctx context.Context, _ auth.IntegrationPrincipal, request authorization.MemberQuery, deliver func(context.Context, authorization.MemberPage) error) error {
	if err := b.check(request.ExpectedRevision); err != nil {
		return err
	}
	if request.ChannelID != 2 || request.AfterID != 1 || request.Search != "Member" {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, authorization.MemberPage{Revision: 7, More: true, Entries: []authorization.MemberIdentity{{UserID: 3, UniqueID: "canonical", Nickname: "Member", RoleIDs: []int64{11}, Manageable: false}}})
}

func (b *inspectionGRPCBackend) WithIntegrationAccessCheck(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.AccessCheck, deliver func(context.Context, netproto.AccessCheckResult) error) error {
	if err := b.check(request.ExpectedRevision); err != nil {
		return err
	}
	if request.ChannelID != 2 || request.UserID != 3 || request.Capability != authorization.Speak {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.AccessCheckResult{CanManageMember: true, Decision: authorization.RoleDecision{Allowed: false, Reason: "prerequisite_denied", RoleIDs: []int64{11}, ChannelID: 2, Requirement: authorization.Connect, Revision: 7}})
}

func (b *inspectionGRPCBackend) WithIntegrationChannelState(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.RoleChannelQuery, deliver func(context.Context, netproto.RoleChannelState) error) error {
	if err := b.check(7); err != nil {
		return err
	}
	if request.Kind != authorization.ChannelMove || request.ChannelID != 2 {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.RoleChannelState{Revision: 7, ChannelID: 2, Name: "Child", AffectedChannels: 3, CanCreatePermanent: true, CanCreateTemporary: true, CanManageAccess: true, EveryoneID: 10,
		Settings: netproto.RoleChannelSettings{Name: "Child", Topic: "Topic", Description: "Description", OrderIndex: -1, MaxClients: 25, SlowModeSeconds: 2, OpusBitrate: 64000, OpusFEC: true, OpusDTX: true, OpusStereo: true},
		Roles:    []netproto.RoleChannelOption{{ID: 11, Name: "Member"}}, GrantableCapabilities: []authorization.Capability{authorization.ViewChannel}, Destinations: []netproto.RoleChannelOption{{ID: 5, Name: "Destination", CanSync: true}},
	})
}

func TestRoleGRPCInspectionContracts(t *testing.T) {
	b := &inspectionGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	conn := dialGRPC(t, startGRPC(t, b, nil))
	legacy := dialGRPC(t, startGRPC(t, &stubBackend{}, nil))
	tests := []struct {
		method            string
		request, response proto.Message
		want              string
	}{
		{noxav1.Control_GetRoleState_FullMethodName, &noxav1.GetRoleStateRequest{ChannelId: 2}, &noxav1.GetRoleStateResponse{}, `{"actorId":"1","parentAccessAvailable":true,"effectiveOverrides":[{"roleId":"10","capability":"view_channel","effect":"allow"}],"parentOverrides":[{"roleId":"10","capability":"view_channel","effect":"allow"}],"manageableRoleIds":["10"],"grantableCapabilities":["view_channel"],"capabilities":[{"key":"send_messages","group":"text","english":"Send messages","german":"Nachrichten senden","channel":true,"requires":["view_channel"]}],"policy":{"revision":"7","ownerId":"1","everyoneId":"10","defaultMemberRoleId":"11","roles":[{"id":"11","name":"Member","position":1,"color":"#123456","icon":"M","hoist":true,"permissions":["view_channel"]}],"members":[{"userId":"3","roleIds":["11"]}],"channels":[{"channelId":"2","parentId":"1","synced":true}]}}`},
		{noxav1.Control_ListRoleMembers_FullMethodName, &noxav1.ListRoleMembersRequest{ChannelId: 2, ExpectedRevision: 7, Search: "Member", AfterId: 1}, &noxav1.ListRoleMembersResponse{}, `{"revision":"7","more":true,"entries":[{"userId":"3","uniqueId":"canonical","nickname":"Member","roleIds":["11"]}]}`},
		{noxav1.Control_CheckAccess_FullMethodName, &noxav1.CheckAccessRequest{UserId: 3, ChannelId: 2, ExpectedRevision: 7, Capability: string(authorization.Speak)}, &noxav1.CheckAccessResponse{}, `{"canManageMember":true,"decision":{"reason":"prerequisite_denied","roleIds":["11"],"channelId":"2","requirement":"connect","revision":"7"}}`},
		{noxav1.Control_GetChannelOptions_FullMethodName, &noxav1.GetChannelOptionsRequest{Kind: string(authorization.ChannelMove), ChannelId: 2}, &noxav1.GetChannelOptionsResponse{}, `{"revision":"7","channelId":"2","name":"Child","affectedChannels":"3","canCreatePermanent":true,"canCreateTemporary":true,"canManageAccess":true,"everyoneId":"10","settings":{"name":"Child","topic":"Topic","description":"Description","orderIndex":-1,"maxClients":25,"slowModeSeconds":2,"opusBitrate":64000,"opusFec":true,"opusDtx":true,"opusStereo":true},"roles":[{"id":"11","name":"Member"}],"grantableCapabilities":["view_channel"],"destinations":[{"id":"5","name":"Destination","canSync":true}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			want := tt.response.ProtoReflect().New().Interface()
			if err := protojson.Unmarshal([]byte(tt.want), want); err != nil {
				t.Fatal(err)
			}
			if err := conn.Invoke(roleAuthCtx(t, "integration", "pw"), tt.method, tt.request, tt.response); err != nil || !proto.Equal(tt.response, want) {
				t.Fatalf("response: %v %v; want %v", tt.response, err, want)
			}
			b.denied.Store(true)
			if err := conn.Invoke(roleAuthCtx(t, "integration", "pw"), tt.method, tt.request, tt.response); status.Code(err) != codes.PermissionDenied {
				t.Fatal(err)
			}
			b.denied.Store(false)
			if err := legacy.Invoke(roleAuthCtx(t, "admin-uid", "pw"), tt.method, tt.request, tt.response); status.Code(err) != codes.FailedPrecondition {
				t.Fatal(err)
			}
		})
	}
	c := noxav1.NewControlClient(conn)
	before := b.calls.Load()
	if _, err := c.GetRoleState(roleAuthCtx(t, "integration", "pw"), &noxav1.GetRoleStateRequest{ChannelId: -1}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	for _, req := range []*noxav1.ListRoleMembersRequest{{}, {ExpectedRevision: 7, ChannelId: -1}, {ExpectedRevision: 7, AfterId: -1}, {ExpectedRevision: 7, Search: strings.Repeat("x", 101)}} {
		if _, err := c.ListRoleMembers(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	for _, req := range []*noxav1.CheckAccessRequest{{}, {ExpectedRevision: 7}, {ExpectedRevision: 7, Capability: "speak", UserId: -1}, {ExpectedRevision: 7, Capability: "speak", ChannelId: -1}} {
		if _, err := c.CheckAccess(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	for _, req := range []*noxav1.GetChannelOptionsRequest{{}, {Kind: "channel_edit", ChannelId: -1}} {
		if _, err := c.GetChannelOptions(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != before {
		t.Fatal("invalid input reached backend")
	}
	if _, err := c.ListRoleMembers(roleAuthCtx(t, "integration", "pw"), &noxav1.ListRoleMembersRequest{ExpectedRevision: 6}); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	if _, err := c.CheckAccess(roleAuthCtx(t, "integration", "pw"), &noxav1.CheckAccessRequest{ExpectedRevision: 6, Capability: "speak"}); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
}
