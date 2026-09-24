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

func (c *controlService) ListCustomMetadata(ctx context.Context, req *noxav1.ListCustomMetadataRequest) (*noxav1.ListCustomMetadataResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleCustomMetadataBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	request := netproto.CustomMetadataQuery{UniqueID: req.GetUniqueId(), AfterKey: req.GetAfterKey(), Limit: int(req.GetLimit())}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "invalid custom metadata page")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListCustomMetadataResponse) error) error {
		return b.WithIntegrationCustomMetadata(ctx, p, request, func(_ context.Context, page netproto.CustomMetadataPage) error {
			result := &noxav1.ListCustomMetadataResponse{UniqueId: page.UniqueID, NextAfterKey: page.NextAfterKey}
			for _, entry := range page.Entries {
				result.Entries = append(result.Entries, &noxav1.CustomMetadataEntry{Key: entry.Key, Value: entry.Value})
			}
			return deliver(result)
		})
	})
}

func (c *controlService) ChangeCustomMetadata(ctx context.Context, req *noxav1.ChangeCustomMetadataRequest) (*noxav1.ChangeCustomMetadataResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleCustomMetadataBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "custom metadata change required")
	}
	request := netproto.CustomMetadataChange{UniqueID: req.UniqueId, Key: req.Key, Value: req.Value, Delete: req.Delete}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "exact subject/key and one metadata action required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.ChangeIntegrationCustomMetadata(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.ChangeCustomMetadataResponse{UniqueId: result.UniqueID, Key: result.Key, Deleted: result.Deleted}, nil
}
