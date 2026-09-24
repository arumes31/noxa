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

func (c *controlService) ListComplaints(ctx context.Context, req *noxav1.ListComplaintsRequest) (*noxav1.ListComplaintsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleComplaintBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetAfterId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid complaint page")
	}
	request := netproto.ComplaintQuery{AfterID: req.GetAfterId(), Limit: int(req.GetLimit())}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListComplaintsResponse) error) error {
		return b.WithIntegrationComplaints(ctx, p, request, func(_ context.Context, page netproto.ComplaintPage) error {
			response := &noxav1.ListComplaintsResponse{NextAfterId: page.NextAfterID}
			for _, e := range page.Entries {
				response.Entries = append(response.Entries, &noxav1.ComplaintRecord{Id: e.ID, TargetUniqueId: e.TargetUniqueID, TargetNickname: e.TargetNickname, FromUniqueId: e.FromUniqueID, FromNickname: e.FromNickname, Reason: e.Reason, CreatedAt: e.CreatedAt})
			}
			return deliver(response)
		})
	})
}

func (c *controlService) ClearComplaints(ctx context.Context, req *noxav1.ClearComplaintsRequest) (*noxav1.ClearComplaintsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleComplaintBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetTargetUniqueId() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_unique_id required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.ClearIntegrationComplaints(effectCtx, p, netproto.ComplaintClear{TargetUniqueID: req.GetTargetUniqueId(), FromUniqueID: req.GetFromUniqueId()})
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.ClearComplaintsResponse{Deleted: result.Deleted}, nil
}
