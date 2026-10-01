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

func (c *controlService) GetServerInfo(ctx context.Context, _ *noxav1.GetServerInfoRequest) (*noxav1.GetServerInfoResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMetadataBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetServerInfoResponse) error) error {
		return b.WithIntegrationServerInfo(ctx, p, func(_ context.Context, info netproto.ServerInfoResponse) error {
			return deliver(&noxav1.GetServerInfoResponse{
				Name: info.Name, Version: info.Version, Platform: info.Platform, UptimeSeconds: info.UptimeSeconds,
				ClientsOnline: int64(info.ClientsOnline), ChannelsOnline: int64(info.ChannelsOnline),
				MaxClients: int64(info.MaxClients), Motd: info.MOTD,
			})
		})
	})
}

func (c *controlService) GetClientInfo(ctx context.Context, req *noxav1.GetClientInfoRequest) (*noxav1.GetClientInfoResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMetadataBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetClientId() == "" {
		return nil, status.Error(codes.InvalidArgument, "client_id required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetClientInfoResponse) error) error {
		return b.WithIntegrationClientInfo(ctx, p, req.GetClientId(), func(_ context.Context, info netproto.ClientInfoResponse) error {
			return deliver(&noxav1.GetClientInfoResponse{
				ClientId: info.ClientID, UniqueId: info.UniqueID, Nickname: info.Nickname, ChannelId: info.ChannelID,
				ConnectedAt: info.ConnectedAt, IdleSeconds: info.IdleSeconds, PingMs: info.PingMs,
				Ip: info.IP, Port: int64(info.Port), BytesIn: info.BytesIn, BytesOut: info.BytesOut,
			})
		})
	})
}

func (c *controlService) GetServerConfig(ctx context.Context, _ *noxav1.GetServerConfigRequest) (*noxav1.GetServerConfigResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMetadataBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetServerConfigResponse) error) error {
		return b.WithIntegrationServerConfig(ctx, p, func(_ context.Context, info netproto.ServerConfig) error {
			return deliver(&noxav1.GetServerConfigResponse{
				MaxClients: int64(info.MaxClients), ClientTimeoutSeconds: int64(info.ClientTimeoutSeconds),
				OpusBitrate: int64(info.OpusBitrate), OpusFec: info.OpusFEC, OpusDtx: info.OpusDTX, OpusStereo: info.OpusStereo,
			})
		})
	})
}

func (c *controlService) ListBans(ctx context.Context, req *noxav1.ListBansRequest) (*noxav1.ListBansResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleBanInspectionBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetBeforeId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "before_id must be nonnegative and limit between 0 and 100")
	}
	request := netproto.BanQuery{BeforeID: req.GetBeforeId(), Limit: int(req.GetLimit())}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListBansResponse) error) error {
		return b.WithIntegrationBans(ctx, p, request, func(_ context.Context, page netproto.BanPage) error {
			result := &noxav1.ListBansResponse{NextBeforeId: page.NextBeforeID}
			for _, ban := range page.Bans {
				result.Bans = append(result.Bans, &noxav1.BanRecord{
					Id: ban.ID, Type: int64(ban.Type), Value: ban.Value, Reason: ban.Reason, BannedBy: ban.BannedBy,
					CreatedAt: ban.CreatedAt, ExpiresAt: ban.ExpiresAt,
				})
			}
			return deliver(result)
		})
	})
}
