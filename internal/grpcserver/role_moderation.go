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

func (c *controlService) DisconnectMember(ctx context.Context, req *noxav1.DisconnectMemberRequest) (*noxav1.DisconnectMemberResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMemberDisconnectBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil || req.GetClientId() == "" || req.GetChannelId() <= 0 || len(req.GetReason()) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "client_id, current channel_id and a reason of at most 4096 bytes are required")
	}
	mutationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.DisconnectIntegrationMember(mutationCtx, p, netproto.MemberDisconnect{ClientID: req.GetClientId(), ChannelID: req.GetChannelId(), Reason: req.GetReason()})
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.DisconnectMemberResponse{ClientId: result.ClientID, ChannelId: result.ChannelID}, nil
}

func (c *controlService) MoveMember(ctx context.Context, req *noxav1.MoveMemberRequest) (*noxav1.MoveMemberResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMemberMoveBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil || req.GetClientId() == "" || req.GetChannelId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "client_id and destination channel_id are required")
	}
	mutationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.MoveIntegrationMember(mutationCtx, p, netproto.MoveClient{ClientID: req.GetClientId(), ChannelID: req.GetChannelId()}); err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.MoveMemberResponse{ClientId: req.GetClientId(), ChannelId: req.GetChannelId()}, nil
}

func (c *controlService) SetMemberVoice(ctx context.Context, req *noxav1.SetMemberVoiceRequest) (*noxav1.SetMemberVoiceResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleVoiceModerationBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req == nil || req.GetClientId() == "" || req.GetChannelId() <= 0 || (req.Muted == nil && req.Deafened == nil) {
		return nil, status.Error(codes.InvalidArgument, "client_id, channel_id and at least one voice flag are required")
	}
	mutationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.SetIntegrationMemberVoice(mutationCtx, p, netproto.MemberVoiceSet{ClientID: req.GetClientId(), ChannelID: req.GetChannelId(), Muted: req.Muted, Deafened: req.Deafened})
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetMemberVoiceResponse{Revision: result.Revision, ClientId: result.ClientID, ChannelId: result.ChannelID, Muted: result.Muted, Deafened: result.Deafened}, nil
}
