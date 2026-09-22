package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) ListAuditLog(ctx context.Context, req *noxav1.ListAuditLogRequest) (*noxav1.ListAuditLogResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleAuditBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetBeforeId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 200 {
		return nil, status.Error(codes.InvalidArgument, "before_id must be nonnegative and limit between 0 and 200")
	}
	request := netproto.AuditLog{BeforeID: req.GetBeforeId(), Limit: int(req.GetLimit())}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListAuditLogResponse) error) error {
		return b.WithIntegrationAudit(ctx, p, request, func(_ context.Context, page netproto.AuditLogResponse) error {
			response := &noxav1.ListAuditLogResponse{}
			for _, e := range page.Entries {
				response.Entries = append(response.Entries, &noxav1.AuditLogRecord{Id: e.ID, Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail, CreatedAt: e.CreatedAt, Restricted: e.Restricted, Structured: e.Structured})
			}
			for _, c := range page.Capabilities {
				response.Capabilities = append(response.Capabilities, &noxav1.CapabilityDescriptor{Key: string(c.Key), Group: c.Group, English: c.English, German: c.German, Channel: c.Channel, Requires: capabilitiesToProto(c.Requires)})
			}
			return deliver(response)
		})
	})
}
