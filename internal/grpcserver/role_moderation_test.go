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
	noxav1 "noxa/v1"
)

type roleVoiceGRPCBackend struct {
	*roleGRPCBackend
	voice      func(netproto.MemberVoiceSet) (netproto.MemberVoiceState, error)
	move       func(netproto.MoveClient) error
	disconnect func(netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error)
}

func (b *roleVoiceGRPCBackend) DisconnectIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error) {
	return b.disconnect(request)
}

func TestRoleGRPCMemberDisconnect(t *testing.T) {
	var calls atomic.Int32
	var denied atomic.Bool
	b := &roleVoiceGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	b.disconnect = func(request netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error) {
		calls.Add(1)
		if denied.Load() {
			return netproto.MemberDisconnectResult{}, authorization.ErrRoleForbidden
		}
		if request.ClientID != "target" || request.ChannelID != 2 || request.Reason != "Take a break" {
			return netproto.MemberDisconnectResult{}, authorization.ErrRoleInvalid
		}
		return netproto.MemberDisconnectResult{ClientID: request.ClientID, ChannelID: request.ChannelID}, nil
	}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	request := &noxav1.DisconnectMemberRequest{ClientId: "target", ChannelId: 2, Reason: "Take a break"}
	result, err := c.DisconnectMember(roleAuthCtx(t, "integration", "pw"), request)
	if err != nil || result.GetClientId() != "target" || result.GetChannelId() != 2 {
		t.Fatalf("committed disconnect: %v %v", result, err)
	}
	denied.Store(true)
	if _, err := c.DisconnectMember(roleAuthCtx(t, "integration", "pw"), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := c.DisconnectMember(roleAuthCtx(t, "integration", "pw"), &noxav1.DisconnectMemberRequest{ClientId: "target"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("invalid scope reached backend")
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.DisconnectMember(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}

func (b *roleVoiceGRPCBackend) MoveIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MoveClient) error {
	return b.move(request)
}

func TestRoleGRPCMemberMove(t *testing.T) {
	var calls atomic.Int32
	var conflict atomic.Bool
	b := &roleVoiceGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	b.move = func(request netproto.MoveClient) error {
		calls.Add(1)
		if conflict.Load() {
			return authorization.ErrRoleConflict
		}
		if request.ClientID != "target" || request.ChannelID != 2 {
			return authorization.ErrRoleInvalid
		}
		return nil
	}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	request := &noxav1.MoveMemberRequest{ClientId: "target", ChannelId: 2}
	result, err := c.MoveMember(roleAuthCtx(t, "integration", "pw"), request)
	if err != nil || result.GetClientId() != "target" || result.GetChannelId() != 2 {
		t.Fatalf("committed move: %v %v", result, err)
	}
	conflict.Store(true)
	if _, err := c.MoveMember(roleAuthCtx(t, "integration", "pw"), request); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	if _, err := c.MoveMember(roleAuthCtx(t, "integration", "pw"), &noxav1.MoveMemberRequest{ClientId: "target"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("invalid destination reached backend")
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.MoveMember(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}

func (b *roleVoiceGRPCBackend) SetIntegrationMemberVoice(_ context.Context, _ auth.IntegrationPrincipal, request netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	return b.voice(request)
}

func TestRoleGRPCMemberVoicePresenceAndDenial(t *testing.T) {
	on, off := true, false
	for _, request := range []netproto.MemberVoiceSet{
		{ClientID: "target", ChannelID: 2, Muted: &on},
		{ClientID: "target", ChannelID: 2, Muted: &off},
		{ClientID: "target", ChannelID: 2, Deafened: &off},
		{ClientID: "target", ChannelID: 2, Muted: &on, Deafened: &off},
	} {
		var calls atomic.Int32
		var denied atomic.Bool
		b := &roleVoiceGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
			return auth.IntegrationPrincipal{}, nil
		}}}
		b.voice = func(got netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
			calls.Add(1)
			if denied.Load() {
				return netproto.MemberVoiceState{}, authorization.ErrRoleForbidden
			}
			if !reflect.DeepEqual(got, request) {
				return netproto.MemberVoiceState{}, authorization.ErrRoleInvalid
			}
			return netproto.MemberVoiceState{Revision: 12, ClientID: "target", ChannelID: 2, Muted: true}, nil
		}
		c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
		req := &noxav1.SetMemberVoiceRequest{ClientId: "target", ChannelId: 2, Muted: request.Muted, Deafened: request.Deafened}
		result, err := c.SetMemberVoice(roleAuthCtx(t, "integration", "pw"), req)
		if err != nil || result.GetRevision() != 12 || result.GetClientId() != "target" || !result.GetMuted() {
			t.Fatalf("voice flags: %v %v", result, err)
		}
		denied.Store(true)
		if _, err := c.SetMemberVoice(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.PermissionDenied {
			t.Fatal(err)
		}
		if _, err := c.SetMemberVoice(roleAuthCtx(t, "integration", "pw"), &noxav1.SetMemberVoiceRequest{ClientId: "target", ChannelId: 2}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
		if calls.Load() != 2 {
			t.Fatal("absent flags reached mutation")
		}
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.SetMemberVoice(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.SetMemberVoiceRequest{ClientId: "target", ChannelId: 2, Muted: &on}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
