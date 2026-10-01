package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type removalGRPCBackend struct {
	*roleGRPCBackend
	denied      atomic.Bool
	unconfirmed atomic.Bool
	calls       atomic.Int32
}

func (b *removalGRPCBackend) KickIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, req netproto.MemberKick) (netproto.MemberKickResult, error) {
	b.calls.Add(1)
	if b.denied.Load() {
		return netproto.MemberKickResult{}, authorization.ErrRoleForbidden
	}
	if req.ClientID != "target" || req.Reason != "Take a break" {
		return netproto.MemberKickResult{}, authorization.ErrRoleInvalid
	}
	return netproto.MemberKickResult{ClientID: req.ClientID, CleanupPending: true}, nil
}

func (b *removalGRPCBackend) BanIntegrationMember(_ context.Context, _ auth.IntegrationPrincipal, req netproto.MemberBan) (netproto.MemberBanResult, error) {
	b.calls.Add(1)
	if b.denied.Load() {
		return netproto.MemberBanResult{}, auth.ErrIntegrationDenied
	}
	if req.ClientID != "target" || req.Reason != "Take a break" || req.DurationSeconds != 60 {
		return netproto.MemberBanResult{}, authorization.ErrRoleInvalid
	}
	result := netproto.MemberBanResult{UniqueID: "canonical-member", Persistence: netproto.BanSaved, ExpiresAt: 1234567890000, CleanupPending: true}
	if b.unconfirmed.Load() {
		result.Persistence, result.ExpiresAt = netproto.BanUnconfirmed, 0
	}
	return result, nil
}

func TestRoleGRPCMemberRemovalOutcomes(t *testing.T) {
	b := &removalGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	kick := &noxav1.KickMemberRequest{ClientId: "target", Reason: "Take a break"}
	kicked, err := client.KickMember(roleAuthCtx(t, "integration", "pw"), kick)
	if err != nil || kicked.GetClientId() != "target" || !kicked.GetCleanupPending() {
		t.Fatalf("kick: %v %v", kicked, err)
	}
	ban := &noxav1.BanMemberRequest{ClientId: "target", Reason: kick.Reason, DurationSeconds: 60}
	for _, unconfirmed := range []bool{false, true} {
		b.unconfirmed.Store(unconfirmed)
		result, err := client.BanMember(roleAuthCtx(t, "integration", "pw"), ban)
		want := noxav1.BanPersistence_BAN_PERSISTENCE_SAVED
		if unconfirmed {
			want = noxav1.BanPersistence_BAN_PERSISTENCE_UNCONFIRMED
		}
		if err != nil || result.GetPersistence() != want || result.GetUniqueId() != "canonical-member" || !result.GetCleanupPending() {
			t.Fatalf("ban: %v %v", result, err)
		}
		if unconfirmed && result.GetExpiresAt() != 0 {
			t.Fatal("unconfirmed ban claimed expiry")
		}
	}
	for _, req := range []*noxav1.BanMemberRequest{{ClientId: "target", DurationSeconds: -1}, {ClientId: "target", DurationSeconds: netproto.MaxBanDurationSeconds + 1}, {DurationSeconds: 60}} {
		if _, err := client.BanMember(roleAuthCtx(t, "integration", "pw"), req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 3 {
		t.Fatal("invalid request reached backend")
	}
	b.denied.Store(true)
	if _, err := client.KickMember(roleAuthCtx(t, "integration", "pw"), kick); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := client.BanMember(roleAuthCtx(t, "integration", "pw"), ban); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.KickMember(roleAuthCtx(t, "admin-uid", "pw"), kick); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := legacy.BanMember(roleAuthCtx(t, "admin-uid", "pw"), ban); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
