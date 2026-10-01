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

func (c *controlService) GetChatFilters(ctx context.Context, _ *noxav1.GetChatFiltersRequest) (*noxav1.GetChatFiltersResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleFilterBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetChatFiltersResponse) error) error {
		return b.WithIntegrationChatFilters(ctx, p, func(_ context.Context, result netproto.ChatFilterResponse) error {
			return deliver(&noxav1.GetChatFiltersResponse{WordFilter: result.WordFilter, LinkBlacklist: result.LinkBlacklist, LinkWhitelist: result.LinkWhitelist, FromConfig: result.FromConfig})
		})
	})
}

func (c *controlService) SetChatFilters(ctx context.Context, req *noxav1.SetChatFiltersRequest) (*noxav1.SetChatFiltersResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleFilterBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "filter patch required")
	}
	patch := netproto.ChatFilterSet{WordFilter: req.WordFilter, LinkBlacklist: req.LinkBlacklist, LinkWhitelist: req.LinkWhitelist}
	if !patch.ValidLists() || (patch.WordFilter == nil && patch.LinkBlacklist == nil && patch.LinkWhitelist == nil) {
		return nil, status.Error(codes.InvalidArgument, "at least one filter list required, each at most 4096 bytes")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.SetIntegrationChatFilters(effectCtx, p, patch)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetChatFiltersResponse{WordFilter: result.WordFilter, LinkBlacklist: result.LinkBlacklist, LinkWhitelist: result.LinkWhitelist, FromConfig: result.FromConfig}, nil
}
