package server

import (
	"context"
	"errors"
	"sort"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/store"
)

type roleChannelWriter interface {
	ChangeRoleChannel(context.Context, int64, authorization.ChannelTreeChange, *store.RoleChannelCreate) (authorization.RolePolicy, int64, error)
	EditRoleChannel(context.Context, int64, int64, int64, store.RoleChannelSettings) (authorization.RolePolicy, error)
}

func (s *TCPServer) handleRoleChannelQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	var query netproto.RoleChannelQuery
	if err := netproto.Decode(f, &query); err != nil || query.ChannelID < 0 {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	if s.deps == nil || s.deps.Authority == nil || s.deps.State == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		response, err := s.buildRoleChannelState(ctx, e.Policy(), e, client.userID(), query)
		if err != nil {
			return s.roleError(ctx, client, err)
		}
		return s.writeMessage(client, netproto.MsgRoleChannelState, response)
	})
}

func (s *TCPServer) buildRoleChannelState(ctx context.Context, p authorization.RolePolicy, e *authorization.RoleEvaluator, actor int64, query netproto.RoleChannelQuery) (netproto.RoleChannelState, error) {
	if query.ChannelID < 0 {
		return netproto.RoleChannelState{}, authorization.ErrRoleInvalid
	}
	// Visibility is mandatory before reading any resource metadata, even
	// when the caller has a management grant or supplies a guessed ID.
	if actor < 1 || !e.Evaluate(actor, query.ChannelID, authorization.ViewChannel).Allowed {
		return netproto.RoleChannelState{}, authorization.ErrRoleForbidden
	}
	response := netproto.RoleChannelState{Revision: p.Revision, ChannelID: query.ChannelID, Capabilities: authorization.Capabilities(), Roles: []netproto.RoleChannelOption{}, Destinations: []netproto.RoleChannelOption{}, GrantableCapabilities: []authorization.Capability{}}
	response.CanCreatePermanent = e.Evaluate(actor, query.ChannelID, authorization.ManageChannels).Allowed
	response.CanCreateTemporary = response.CanCreatePermanent || e.Evaluate(actor, query.ChannelID, authorization.CreateTemporaryChannels).Allowed
	response.CanManageAccess = e.Evaluate(actor, query.ChannelID, authorization.ManageChannelAccess).Allowed
	switch query.Kind {
	case authorization.ChannelCreate:
		if !response.CanCreateTemporary {
			return netproto.RoleChannelState{}, authorization.ErrRoleForbidden
		}
		s.configMu.RLock()
		response.Settings = netproto.RoleChannelSettings{OpusBitrate: s.cfg.DefaultOpusBitrate, OpusFEC: s.cfg.DefaultOpusFEC, OpusDTX: s.cfg.DefaultOpusDTX, OpusStereo: s.cfg.DefaultOpusStereo}
		s.configMu.RUnlock()
	case authorization.ChannelMove, authorization.ChannelDelete:
		// Check every descendant before revealing the subtree count.
		deleted, err := authorization.ApplyChannelTreeChange(p, actor, authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ChannelID: query.ChannelID, ExpectedRevision: p.Revision})
		if err != nil {
			return netproto.RoleChannelState{}, authorization.ErrRoleForbidden
		}
		response.AffectedChannels = len(p.Channels) - len(deleted.Channels)
	case authorization.ChannelEdit:
		if query.ChannelID == 0 || !response.CanCreatePermanent {
			return netproto.RoleChannelState{}, authorization.ErrRoleForbidden
		}
	default:
		return netproto.RoleChannelState{}, authorization.ErrRoleInvalid
	}
	if query.ChannelID > 0 {
		ch, exists := s.deps.State.GetChannel(query.ChannelID)
		if !exists {
			return netproto.RoleChannelState{}, authorization.ErrAuthorizationUnavailable
		}
		response.Name = ch.Name
		if query.Kind != authorization.ChannelCreate {
			response.Settings = roleChannelSettings(ch)
		}
	}
	if query.Kind == authorization.ChannelCreate && response.CanManageAccess {
		response.EveryoneID = p.EveryoneID
		for _, role := range p.Roles {
			if err := ctx.Err(); err != nil {
				return netproto.RoleChannelState{}, err
			}
			if role.ID != p.EveryoneID && e.CanManageChannelRole(actor, query.ChannelID, role.ID) {
				response.Roles = append(response.Roles, netproto.RoleChannelOption{ID: role.ID, Name: role.Name})
			}
		}
		for _, capability := range authorization.Capabilities() {
			if capability.Channel && e.Evaluate(actor, query.ChannelID, capability.Key).Allowed {
				response.GrantableCapabilities = append(response.GrantableCapabilities, capability.Key)
			}
		}
	}
	if query.Kind == authorization.ChannelMove {
		if response.CanManageAccess {
			var err error
			response.ImpactChannelIDs, err = e.ImpactDescendants(ctx, actor, query.ChannelID)
			if err != nil {
				return netproto.RoleChannelState{}, err
			}
		}
		for _, candidate := range append([]*state.Channel{{ChannelID: 0}}, s.deps.State.ListChannels()...) {
			if err := ctx.Err(); err != nil {
				return netproto.RoleChannelState{}, err
			}
			if !e.Evaluate(actor, candidate.ChannelID, authorization.ViewChannel).Allowed {
				continue
			}
			change := authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ChannelID: query.ChannelID, ParentID: candidate.ChannelID, ExpectedRevision: p.Revision}
			if _, err := authorization.ApplyChannelTreeChange(p, actor, change); err != nil {
				continue
			}
			change.SyncToParent = true
			_, syncErr := authorization.ApplyChannelTreeChange(p, actor, change)
			response.Destinations = append(response.Destinations, netproto.RoleChannelOption{ID: candidate.ChannelID, Name: candidate.Name, CanSync: syncErr == nil})
		}
		sort.Slice(response.Destinations, func(i, j int) bool { return response.Destinations[i].ID < response.Destinations[j].ID })
	}
	return response, nil
}

func roleChannelSettings(ch *state.Channel) netproto.RoleChannelSettings {
	return netproto.RoleChannelSettings{Name: ch.Name, Topic: ch.Topic, Description: ch.Description, OrderIndex: ch.OrderIndex, MaxClients: ch.MaxClients, SlowModeSeconds: ch.SlowModeSeconds, OpusBitrate: ch.OpusBitrate, OpusFEC: ch.OpusFEC, OpusDTX: ch.OpusDTX, OpusStereo: ch.OpusStereo}
}

func (s *TCPServer) handleRoleChannelChange(ctx context.Context, client *Client, f *netproto.Frame) error {
	var request netproto.RoleChannelChange
	if err := netproto.Decode(f, &request); err != nil {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	result, err := s.changeRoleChannel(ctx, client.userID(), request, func(context.Context) error {
		if client.sessionRevoked() || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		return nil
	})
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgRoleChannelResult, result)
}

// validate, when present, rechecks integration admission before hashing and
// again under the exclusive mutation gate. It must not retain metadata locks.
func (s *TCPServer) changeRoleChannel(ctx context.Context, actor int64, request netproto.RoleChannelChange, validate func(context.Context) error) (netproto.RoleChannelResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.deps == nil || s.deps.Authority == nil {
		return netproto.RoleChannelResult{}, authorization.ErrRolesNotConfigured
	}
	writer, ok := s.deps.Channels.(roleChannelWriter)
	if !ok {
		return netproto.RoleChannelResult{}, authorization.ErrAuthorizationUnavailable
	}
	if request.OrderIndex != nil && request.Kind != authorization.ChannelMove {
		return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
	}
	change := authorization.ChannelTreeChange{Kind: request.Kind, ExpectedRevision: request.ExpectedRevision, ChannelID: request.ChannelID, ParentID: request.ParentID, SyncToParent: request.SyncToParent, OrderIndex: request.OrderIndex}
	var settings store.RoleChannelSettings
	if request.Settings != nil {
		settings = store.RoleChannelSettings(*request.Settings)
		if !settings.Valid() {
			return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
		}
	}
	var create *store.RoleChannelCreate
	switch request.Kind {
	case authorization.ChannelCreate:
		if request.Settings == nil || request.ChannelID != 0 || request.SyncToParent || request.ChannelType < 0 || request.ChannelType > 2 || len(request.Password) > 4096 {
			return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
		}
		change.Temporary = request.ChannelType == 0
		change.Access = authorization.ChannelPolicy{ParentID: request.ParentID, Synced: true}
		if request.Access != nil {
			change.Access.Synced, change.Access.Overrides = request.Access.Synced, request.Access.Overrides
		}
		// Preflight before expensive hashing. Storage authorizes the same actor
		// and revision again inside its transaction under Authority's barrier.
		err := s.withRolePolicy(ctx, func(ctx context.Context) error {
			if validate != nil {
				if err := validate(ctx); err != nil {
					return err
				}
			}
			p := ctx.Value(roleLeaseKey{}).(roleLease).evaluator.Policy()
			preview := change
			preview.ChannelID = 1
			for _, ch := range p.Channels {
				if ch.ChannelID >= preview.ChannelID {
					preview.ChannelID = ch.ChannelID + 1
				}
			}
			preview.Access.ChannelID = preview.ChannelID
			_, err := authorization.ApplyChannelTreeChange(p, actor, preview)
			return err
		})
		if err != nil {
			return netproto.RoleChannelResult{}, err
		}
		passwordHash := ""
		if request.Password != "" {
			passwordHash, err = auth.HashPassword(request.Password)
			if err != nil {
				return netproto.RoleChannelResult{}, err
			}
		}
		create = &store.RoleChannelCreate{Name: settings.Name, Topic: settings.Topic, Description: settings.Description, OrderIndex: settings.OrderIndex, ChannelType: request.ChannelType, MaxClients: settings.MaxClients, SlowModeSeconds: settings.SlowModeSeconds, PasswordHash: passwordHash, OpusBitrate: settings.OpusBitrate, OpusFEC: settings.OpusFEC, OpusDTX: settings.OpusDTX, OpusStereo: settings.OpusStereo}
	case authorization.ChannelEdit, authorization.ChannelMove, authorization.ChannelDelete:
		if request.ChannelID < 1 || request.Access != nil || request.Password != "" || request.ChannelType != 0 || (request.Kind == authorization.ChannelEdit) != (request.Settings != nil) || (request.Kind != authorization.ChannelMove && (request.ParentID != 0 || request.SyncToParent)) {
			return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
		}
	default:
		return netproto.RoleChannelResult{}, authorization.ErrRoleInvalid
	}
	channelID := request.ChannelID
	p, err := s.deps.Authority.ChangeLifecyclePolicyValidated(ctx, request.ExpectedRevision, validate, func(ctx context.Context) (authorization.RolePolicy, error) {
		if request.Kind == authorization.ChannelEdit {
			return writer.EditRoleChannel(ctx, actor, request.ChannelID, request.ExpectedRevision, settings)
		}
		p, id, err := writer.ChangeRoleChannel(ctx, actor, change, create)
		channelID = id
		return p, err
	})
	if err != nil && !errors.Is(err, authorization.ErrEnforcementPending) {
		return netproto.RoleChannelResult{}, err
	}
	return netproto.RoleChannelResult{Revision: p.Revision, ChannelID: channelID, EnforcementPending: errors.Is(err, authorization.ErrEnforcementPending)}, nil
}
