package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) ListClients(ctx context.Context, _ *noxav1.ListClientsRequest) (*noxav1.ListClientsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleIntegrationBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListClientsResponse) error) error {
		return b.WithIntegrationSnapshot(ctx, p, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
			response := &noxav1.ListClientsResponse{}
			add := func(clients []*broadcast.ClientInfo) {
				for _, client := range clients {
					response.Clients = append(response.Clients, &noxav1.VisibleClient{ClientId: client.ClientID, UniqueId: client.UniqueID, Nickname: client.Nickname, ChannelId: client.ChannelID})
				}
			}
			add(snapshot.UnassignedClients)
			var visit func([]*broadcast.ChannelNode) error
			visit = func(nodes []*broadcast.ChannelNode) error {
				for _, node := range nodes {
					if err := ctx.Err(); err != nil {
						return err
					}
					add(node.Clients)
					if err := visit(node.Children); err != nil {
						return err
					}
				}
				return nil
			}
			if err := visit(snapshot.RootChannels); err != nil {
				return err
			}
			return deliver(response)
		})
	})
}

func (c *controlService) GetChannelInfo(ctx context.Context, req *noxav1.GetChannelInfoRequest) (*noxav1.GetChannelInfoResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleIntegrationBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetChannelId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "channel_id must be positive")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetChannelInfoResponse) error) error {
		return b.WithIntegrationSnapshot(ctx, p, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
			// Inspect only the already-filtered projection: missing and hidden
			// channels have the same result, including hidden parent identifiers.
			stack := append([]*broadcast.ChannelNode(nil), snapshot.RootChannels...)
			for len(stack) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				node := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if node.ChannelID == req.GetChannelId() {
					return deliver(&noxav1.GetChannelInfoResponse{
						ChannelId: node.ChannelID, ParentId: node.ParentID, Name: node.Name, Topic: node.Topic,
						ChannelType: int64(node.ChannelType), MaxClients: int64(node.MaxClients), CurrentClients: int64(node.ClientCount),
						OpusBitrate: int64(node.OpusBitrate), OpusFec: node.OpusFEC, OpusDtx: node.OpusDTX, OpusStereo: node.OpusStereo, SlowModeSeconds: int64(node.SlowModeSeconds),
					})
				}
				stack = append(stack, node.Children...)
			}
			return authorization.ErrRoleForbidden
		})
	})
}
