package grpcserver

import (
	"context"
	"math"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) GetRoleState(ctx context.Context, req *noxav1.GetRoleStateRequest) (*noxav1.GetRoleStateResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleManagementBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetChannelId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "channel_id must be nonnegative")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetRoleStateResponse) error) error {
		return b.WithIntegrationRoleState(ctx, p, req.GetChannelId(), func(_ context.Context, state netproto.RoleState) error {
			response, err := roleStateToProto(state)
			if err != nil {
				return err
			}
			return deliver(response)
		})
	})
}

func (c *controlService) ListRoleMembers(ctx context.Context, req *noxav1.ListRoleMembersRequest) (*noxav1.ListRoleMembersResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleInspectionBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetExpectedRevision() <= 0 || req.GetChannelId() < 0 || req.GetAfterId() < 0 || len(req.GetSearch()) > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid member query")
	}
	request := authorization.MemberQuery{ChannelID: req.GetChannelId(), ExpectedRevision: req.GetExpectedRevision(), Search: req.GetSearch(), AfterID: req.GetAfterId()}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListRoleMembersResponse) error) error {
		return b.WithIntegrationRoleMembers(ctx, p, request, func(_ context.Context, page authorization.MemberPage) error {
			response := &noxav1.ListRoleMembersResponse{Revision: page.Revision, More: page.More}
			for _, member := range page.Entries {
				response.Entries = append(response.Entries, &noxav1.RoleMemberIdentity{UserId: member.UserID, UniqueId: member.UniqueID, Nickname: member.Nickname, RoleIds: slices.Clone(member.RoleIDs), Manageable: member.Manageable})
			}
			return deliver(response)
		})
	})
}

func (c *controlService) CheckAccess(ctx context.Context, req *noxav1.CheckAccessRequest) (*noxav1.CheckAccessResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleInspectionBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetExpectedRevision() <= 0 || req.GetChannelId() < 0 || req.GetUserId() < 0 || req.GetCapability() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid access query")
	}
	request := netproto.AccessCheck{UserID: req.GetUserId(), ChannelID: req.GetChannelId(), Capability: authorization.Capability(req.GetCapability()), ExpectedRevision: req.GetExpectedRevision()}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.CheckAccessResponse) error) error {
		return b.WithIntegrationAccessCheck(ctx, p, request, func(_ context.Context, result netproto.AccessCheckResult) error {
			d := result.Decision
			return deliver(&noxav1.CheckAccessResponse{CanManageMember: result.CanManageMember, Decision: &noxav1.RoleAccessDecision{Allowed: d.Allowed, Reason: d.Reason, RoleIds: slices.Clone(d.RoleIDs), ChannelId: d.ChannelID, Requirement: string(d.Requirement), Revision: d.Revision}})
		})
	})
}

func (c *controlService) GetChannelOptions(ctx context.Context, req *noxav1.GetChannelOptionsRequest) (*noxav1.GetChannelOptionsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleChannelBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetKind() == "" || req.GetChannelId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid channel query")
	}
	request := netproto.RoleChannelQuery{Kind: authorization.ChannelChangeKind(req.GetKind()), ChannelID: req.GetChannelId()}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetChannelOptionsResponse) error) error {
		return b.WithIntegrationChannelState(ctx, p, request, func(_ context.Context, state netproto.RoleChannelState) error {
			s := state.Settings
			if s.OrderIndex < math.MinInt32 || s.OrderIndex > math.MaxInt32 ||
				s.MaxClients < 0 || s.MaxClients > math.MaxInt32 ||
				s.SlowModeSeconds < 0 || s.SlowModeSeconds > math.MaxInt32 ||
				s.OpusBitrate < 0 || s.OpusBitrate > math.MaxInt32 {
				return status.Error(codes.Internal, "channel settings exceed protobuf range")
			}
			return deliver(&noxav1.GetChannelOptionsResponse{
				Revision: state.Revision, ChannelId: state.ChannelID, Name: state.Name, AffectedChannels: int64(state.AffectedChannels),
				CanCreatePermanent: state.CanCreatePermanent, CanCreateTemporary: state.CanCreateTemporary, CanManageAccess: state.CanManageAccess, EveryoneId: state.EveryoneID,
				Roles: channelOptionsToProto(state.Roles), GrantableCapabilities: capabilitiesToProto(state.GrantableCapabilities), Destinations: channelOptionsToProto(state.Destinations),
				Settings: &noxav1.RoleChannelSettings{Name: s.Name, Topic: s.Topic, Description: s.Description, OrderIndex: int32(s.OrderIndex), MaxClients: int32(s.MaxClients), SlowModeSeconds: int32(s.SlowModeSeconds), OpusBitrate: int32(s.OpusBitrate), OpusFec: s.OpusFEC, OpusDtx: s.OpusDTX, OpusStereo: s.OpusStereo},
			})
		})
	})
}

func capabilitiesToProto(values []authorization.Capability) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}

func overridesToProto(values []authorization.RoleOverride) []*noxav1.ChannelRoleOverride {
	result := make([]*noxav1.ChannelRoleOverride, len(values))
	for i, value := range values {
		result[i] = &noxav1.ChannelRoleOverride{RoleId: value.RoleID, UserId: value.UserID, Capability: string(value.Capability), Effect: string(value.Effect)}
	}
	return result
}

func channelOptionsToProto(values []netproto.RoleChannelOption) []*noxav1.ChannelManagementOption {
	result := make([]*noxav1.ChannelManagementOption, len(values))
	for i, value := range values {
		result[i] = &noxav1.ChannelManagementOption{Id: value.ID, Name: value.Name, CanSync: value.CanSync}
	}
	return result
}

func roleStateToProto(state netproto.RoleState) (*noxav1.GetRoleStateResponse, error) {
	p := state.Policy
	policy := &noxav1.RolePolicySnapshot{Revision: p.Revision, OwnerId: p.OwnerID, EveryoneId: p.EveryoneID, DefaultMemberRoleId: p.DefaultMemberRoleID}
	for _, r := range p.Roles {
		if r.Position < 0 || r.Position > math.MaxInt32 {
			return nil, status.Error(codes.Internal, "role position exceeds protobuf range")
		}
		policy.Roles = append(policy.Roles, &noxav1.RoleDefinition{Id: r.ID, Name: r.Name, Position: int32(r.Position), Color: r.Color, Icon: r.Icon, Hoist: r.Hoist, Mentionable: r.Mentionable, Permissions: capabilitiesToProto(r.Permissions)})
	}
	for _, m := range p.Members {
		policy.Members = append(policy.Members, &noxav1.RoleAssignment{UserId: m.UserID, RoleIds: slices.Clone(m.RoleIDs)})
	}
	for _, ch := range p.Channels {
		policy.Channels = append(policy.Channels, &noxav1.ChannelRoleAccess{ChannelId: ch.ChannelID, ParentId: ch.ParentID, Synced: ch.Synced, Overrides: overridesToProto(ch.Overrides)})
	}
	result := &noxav1.GetRoleStateResponse{ParentAccessAvailable: state.ParentAccessAvailable, EffectiveOverrides: overridesToProto(state.EffectiveOverrides), ParentOverrides: overridesToProto(state.ParentOverrides), ActorId: state.ActorID, Policy: policy, ManageableRoleIds: slices.Clone(state.ManageableRoleIDs), GrantableCapabilities: capabilitiesToProto(state.GrantableCapabilities)}
	for _, c := range state.Capabilities {
		result.Capabilities = append(result.Capabilities, &noxav1.CapabilityDescriptor{Key: string(c.Key), Group: c.Group, English: c.English, German: c.German, Channel: c.Channel, Requires: capabilitiesToProto(c.Requires)})
	}
	return result, nil
}
