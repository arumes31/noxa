package grpcserver

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) KickMember(ctx context.Context, req *noxav1.KickMemberRequest) (*noxav1.KickMemberResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMemberRemovalBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil || req.GetClientId() == "" || len(req.GetReason()) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "client_id and a reason of at most 4096 bytes are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := b.KickIntegrationMember(ctx, p, netproto.MemberKick{ClientID: req.GetClientId(), Reason: req.GetReason()})
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.KickMemberResponse{ClientId: result.ClientID, CleanupPending: result.CleanupPending}, nil
}

func (c *controlService) BanMember(ctx context.Context, req *noxav1.BanMemberRequest) (*noxav1.BanMemberResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMemberRemovalBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil || req.GetClientId() == "" || len(req.GetReason()) > 4096 || req.GetDurationSeconds() < 0 || req.GetDurationSeconds() > netproto.MaxBanDurationSeconds {
		return nil, status.Error(codes.InvalidArgument, "client_id, valid duration_seconds and a reason of at most 4096 bytes are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := b.BanIntegrationMember(ctx, p, netproto.MemberBan{ClientID: req.GetClientId(), Reason: req.GetReason(), DurationSeconds: req.GetDurationSeconds()})
	if err != nil {
		return nil, roleStatus(err)
	}
	persistence := noxav1.BanPersistence_BAN_PERSISTENCE_UNCONFIRMED
	if result.Persistence == netproto.BanSaved {
		persistence = noxav1.BanPersistence_BAN_PERSISTENCE_SAVED
	}
	return &noxav1.BanMemberResponse{UniqueId: result.UniqueID, Persistence: persistence, ExpiresAt: result.ExpiresAt, CleanupPending: result.CleanupPending}, nil
}
