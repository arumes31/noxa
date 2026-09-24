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

func (c *controlService) GetServerRules(ctx context.Context, _ *noxav1.GetServerRulesRequest) (*noxav1.GetServerRulesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleRulesBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetServerRulesResponse) error) error {
		return b.WithIntegrationRules(ctx, p, func(_ context.Context, result netproto.RulesInspection) error {
			return deliver(&noxav1.GetServerRulesResponse{Text: result.Text, Hash: result.Hash, AcceptedClients: int64(result.AcceptedClients)})
		})
	})
}
