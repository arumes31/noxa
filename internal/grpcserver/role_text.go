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

func (c *controlService) SetServerText(ctx context.Context, req *noxav1.SetServerTextRequest) (*noxav1.SetServerTextResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleTextBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "server text required")
	}
	request := netproto.ServerTextSet{Key: req.Key, Value: req.Value}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "supported key and valid text required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.SetIntegrationServerText(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetServerTextResponse{Key: result.Key, ContentHash: result.ContentHash}, nil
}
