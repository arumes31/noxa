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

func (c *controlService) SetServerConfig(ctx context.Context, req *noxav1.SetServerConfigRequest) (*noxav1.SetServerConfigResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleConfigBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	request := netproto.ServerConfig{MaxClients: int(req.GetMaxClients()), ClientTimeoutSeconds: int(req.GetClientTimeoutSeconds()), OpusBitrate: int(req.GetOpusBitrate()), OpusFEC: req.GetOpusFec(), OpusDTX: req.GetOpusDtx(), OpusStereo: req.GetOpusStereo()}
	if !request.ValidLimits() {
		return nil, status.Error(codes.InvalidArgument, "invalid server configuration limits")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.SetIntegrationServerConfig(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetServerConfigResponse{MaxClients: int64(result.MaxClients), ClientTimeoutSeconds: int64(result.ClientTimeoutSeconds), OpusBitrate: int64(result.OpusBitrate), OpusFec: result.OpusFEC, OpusDtx: result.OpusDTX, OpusStereo: result.OpusStereo}, nil
}
