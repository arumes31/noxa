package grpcserver

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) ChangeChannel(ctx context.Context, req *noxav1.ChangeChannelRequest) (*noxav1.ChangeChannelResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleChannelBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetExpectedRevision() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "expected_revision must be positive")
	}
	change := channelChangeFromProto(req)
	mutationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := b.ChangeIntegrationChannel(mutationCtx, p, change)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.ChangeChannelResponse{Revision: result.Revision, ChannelId: result.ChannelID, EnforcementPending: result.EnforcementPending}, nil
}

func channelChangeFromProto(req *noxav1.ChangeChannelRequest) netproto.RoleChannelChange {
	change := netproto.RoleChannelChange{
		Kind: authorization.ChannelChangeKind(req.GetKind()), ExpectedRevision: req.GetExpectedRevision(),
		ChannelID: req.GetChannelId(), ParentID: req.GetParentId(), SyncToParent: req.GetSyncToParent(),
		ChannelType: int(req.GetChannelType()), Password: req.GetPassword(),
	}
	if req.OrderIndex != nil {
		order := req.GetOrderIndex()
		change.OrderIndex = &order
	}
	if settings := req.GetSettings(); settings != nil {
		change.Settings = &netproto.RoleChannelSettings{
			Name: settings.GetName(), Topic: settings.GetTopic(), Description: settings.GetDescription(),
			OrderIndex: int(settings.GetOrderIndex()), MaxClients: int(settings.GetMaxClients()), SlowModeSeconds: int(settings.GetSlowModeSeconds()),
			OpusBitrate: int(settings.GetOpusBitrate()), OpusFEC: settings.GetOpusFec(), OpusDTX: settings.GetOpusDtx(), OpusStereo: settings.GetOpusStereo(),
		}
	}
	if access := req.GetAccess(); access != nil {
		change.Access = &netproto.RoleChannelAccess{Synced: access.GetSynced()}
		for _, override := range access.GetOverrides() {
			change.Access.Overrides = append(change.Access.Overrides, authorization.RoleOverride{
				RoleID: override.GetRoleId(), UserID: override.GetUserId(), Capability: authorization.Capability(override.GetCapability()), Effect: authorization.OverrideEffect(override.GetEffect()),
			})
		}
	}
	return change
}
