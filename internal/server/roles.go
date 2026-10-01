package server

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// RoleStore must perform authority checks, writes and audit atomically. The
// authenticated actor ID comes from Client, never from the request body.
type RoleStore interface {
	RolePolicy(context.Context) (authorization.RolePolicy, error)
	ChangeRolePolicy(context.Context, int64, authorization.RoleChange) (authorization.RolePolicy, error)
}

type RoleMemberStore interface {
	RoleMembers(context.Context, int64, authorization.MemberQuery) (authorization.MemberPage, error)
}

func (s *TCPServer) handleRoleMembers(ctx context.Context, client *Client, f *netproto.Frame) error {
	var query authorization.MemberQuery
	if err := netproto.Decode(f, &query); err != nil {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	if s.deps == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	roster, ok := s.deps.Roles.(RoleMemberStore)
	if !ok {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	page, err := roster.RoleMembers(ctx, client.userID(), query)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgRoleMembers, page)
}

func (s *TCPServer) readRolePolicy(ctx context.Context) (authorization.RolePolicy, *authorization.RoleEvaluator, error) {
	if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok && s.deps != nil && lease.authority == s.deps.Authority {
		return lease.evaluator.Policy(), lease.evaluator, nil
	}
	backend := s.roleBackend()
	if backend == nil {
		return authorization.RolePolicy{}, nil, authorization.ErrRolesNotConfigured
	}
	p, err := backend.RolePolicy(ctx)
	if err != nil {
		return p, nil, err
	}
	e, err := authorization.NewRoleEvaluator(p)
	return p, e, err
}

func (s *TCPServer) roleBackend() RoleStore {
	if s.deps == nil || s.deps.Authority == nil {
		return nil
	}
	return s.deps.Authority
}

func (s *TCPServer) roleError(ctx context.Context, client *Client, err error) error {
	code, message := uint16(errCodeUnavailable), "role authorization unavailable"
	switch {
	case errors.Is(err, authorization.ErrRoleForbidden):
		code, message = errCodePermissionDenied, "you cannot manage this role or channel"
	case errors.Is(err, authorization.ErrRoleConflict):
		code, message = errCodeConflict, "permissions changed; refresh before saving"
	case errors.Is(err, authorization.ErrRoleInvalid):
		code, message = errCodeMalformed, "invalid role change"
	case errors.Is(err, authorization.ErrRolesNotConfigured):
		message = "role authorization has not been set up"
	default:
		s.logger.Warn("role authorization failed", zap.Error(err))
	}
	return s.sendErrorFor(client, requestOrigin(ctx), code, message)
}

func (s *TCPServer) handleRoleQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	var query netproto.RoleQuery
	if err := netproto.Decode(f, &query); err != nil || query.ChannelID < 0 {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	p, e, err := s.readRolePolicy(ctx)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	resp, err := buildRoleState(ctx, p, e, client.userID(), query.ChannelID)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgRoleState, resp)
}

func buildRoleState(ctx context.Context, p authorization.RolePolicy, e *authorization.RoleEvaluator, actorID, channelID int64) (netproto.RoleState, error) {
	if channelID < 0 {
		return netproto.RoleState{}, authorization.ErrRoleInvalid
	}
	capability := authorization.ManageRoles
	if channelID > 0 {
		capability = authorization.ManageChannelAccess
	}
	if actorID < 1 || !e.Evaluate(actorID, channelID, capability).Allowed {
		return netproto.RoleState{}, authorization.ErrRoleForbidden
	}
	// A scoped editor sees only that channel. The roles editor receives only
	// policies the actor can manage; neither path exposes hidden resource IDs.
	visible := []authorization.ChannelPolicy{}
	var parentID int64
	for _, ch := range p.Channels {
		if err := ctx.Err(); err != nil {
			return netproto.RoleState{}, err
		}
		if (channelID > 0 && ch.ChannelID != channelID) || !e.Evaluate(actorID, ch.ChannelID, authorization.ManageChannelAccess).Allowed {
			continue
		}
		if ch.ChannelID == channelID {
			parentID = ch.ParentID
		}
		if ch.ParentID != 0 && !e.Evaluate(actorID, ch.ParentID, authorization.ViewChannel).Allowed {
			ch.ParentID = 0
		}
		visible = append(visible, ch)
	}
	p.Channels = visible
	resp := netproto.RoleState{ActorID: actorID, Policy: p, Capabilities: authorization.Capabilities(), ManageableRoleIDs: []int64{}}
	if channelID > 0 {
		var err error
		resp.ImpactChannelIDs, err = e.ImpactDescendants(ctx, actorID, channelID)
		if err != nil {
			return netproto.RoleState{}, err
		}
	}
	if channelID > 0 && len(p.Channels) == 1 {
		resp.EffectiveOverrides = e.EffectiveOverrides(channelID)
		resp.ParentAccessAvailable = parentID > 0 && e.Evaluate(actorID, parentID, authorization.ManageChannelAccess).Allowed
		if resp.ParentAccessAvailable {
			resp.ParentOverrides = e.EffectiveOverrides(parentID)
		}
	}
	for _, c := range resp.Capabilities {
		if c.Key == authorization.Administrator && actorID != p.OwnerID {
			continue
		}
		if e.Evaluate(actorID, channelID, c.Key).Allowed {
			resp.GrantableCapabilities = append(resp.GrantableCapabilities, c.Key)
		}
	}
	for _, role := range p.Roles {
		if err := ctx.Err(); err != nil {
			return netproto.RoleState{}, err
		}
		if (channelID == 0 && e.CanManageRole(actorID, role.ID)) ||
			(channelID > 0 && e.CanManageChannelRole(actorID, channelID, role.ID)) {
			resp.ManageableRoleIDs = append(resp.ManageableRoleIDs, role.ID)
		}
	}
	return resp, nil
}

func (s *TCPServer) handleRoleChange(ctx context.Context, client *Client, f *netproto.Frame) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var change authorization.RoleChange
	if err := netproto.Decode(f, &change); err != nil {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	backend := s.roleBackend()
	if backend == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	var p authorization.RolePolicy
	var err error
	p, err = s.deps.Authority.ChangeRolePolicyValidated(ctx, client.userID(), change, func(context.Context) error {
		if client.sessionRevoked() || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		return nil
	})
	if err != nil && !errors.Is(err, authorization.ErrEnforcementPending) {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgRoleChangeResult, roleChangeResult(p, change, err))
}

func roleChangeResult(p authorization.RolePolicy, change authorization.RoleChange, err error) netproto.RoleChangeResult {
	resp := netproto.RoleChangeResult{Revision: p.Revision, EnforcementPending: errors.Is(err, authorization.ErrEnforcementPending)}
	if change.Kind == authorization.RoleCreate {
		// Creation inserts immediately above @everyone, independent of IDs.
		for _, r := range p.Roles {
			if r.Position == 1 {
				resp.CreatedRoleID = r.ID
				break
			}
		}
	}
	return resp
}

func (s *TCPServer) handleAccessCheck(ctx context.Context, client *Client, f *netproto.Frame) error {
	var query netproto.AccessCheck
	if err := netproto.Decode(f, &query); err != nil || query.UserID < 0 || query.ChannelID < 0 {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	_, e, err := s.readRolePolicy(ctx)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	response, err := buildAccessCheck(e, client.userID(), query)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgAccessCheckResult, response)
}

func buildAccessCheck(e *authorization.RoleEvaluator, actorID int64, query netproto.AccessCheck) (netproto.AccessCheckResult, error) {
	if query.UserID < 0 || query.ChannelID < 0 {
		return netproto.AccessCheckResult{}, authorization.ErrRoleInvalid
	}
	capability := authorization.ManageRoles
	if query.ChannelID > 0 {
		capability = authorization.ManageChannelAccess
	}
	actor := e.Evaluate(actorID, query.ChannelID, capability)
	if actorID < 1 || !actor.Allowed {
		return netproto.AccessCheckResult{}, authorization.ErrRoleForbidden
	}
	if query.ExpectedRevision != actor.Revision {
		return netproto.AccessCheckResult{}, authorization.ErrRoleConflict
	}
	return netproto.AccessCheckResult{
		Decision:        e.Evaluate(query.UserID, query.ChannelID, query.Capability),
		CanManageMember: e.CanManageMember(actorID, query.UserID),
	}, nil
}
