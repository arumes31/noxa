package server

import (
	"context"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// A device means its authenticated control session, never an unverified device
// label. Reconnect establishes a new session. Guests and bots do not get relay.
func (s *TCPServer) relayAllowed(e *authorization.RoleEvaluator, client *Client) bool {
	if e == nil || client == nil || !client.isAuthed() || client.sessionRevoked() || client.rulesBlocked() || client.userID() <= 0 {
		return false
	}
	if last := client.lastActivity(); !last.IsZero() && time.Since(last) > 45*time.Second {
		return false
	}
	client.mu.RLock()
	bot := client.bot
	client.mu.RUnlock()
	if bot {
		return false
	}
	if e.Evaluate(client.userID(), 0, authorization.Connect).Allowed {
		return true
	}
	for _, channel := range s.deps.State.ListChannels() {
		if e.Evaluate(client.userID(), channel.ChannelID, authorization.Connect).Allowed && e.Evaluate(client.userID(), channel.ChannelID, authorization.ViewChannel).Allowed {
			return true
		}
	}
	return false
}
func (s *TCPServer) reconcileRelay(e *authorization.RoleEvaluator) {
	if s.deps == nil || s.deps.Relay == nil || s.deps.State == nil {
		return
	}
	for _, member := range s.deps.State.ListClients() {
		client, ok := s.clientByID(member.ClientID)
		s.deps.Relay.Authorize(member.ClientID, ok && s.relayAllowed(e, client))
	}
}

// The authorization lease expires independently at the relay if the control
// authority cannot be consulted. No database write or per-packet RPC is needed.
func (s *TCPServer) MaintainRelayAuthorization(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.deps.Authority != nil {
				_ = s.deps.Authority.TryWithPolicy(func(e *authorization.RoleEvaluator) error { s.reconcileRelay(e); return nil })
			}
		}
	}
}
func (s *TCPServer) handleRelay(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var request netproto.RelayRequest
	if netproto.Decode(frame, &request) != nil || (request.Action != "refresh" && request.Action != "revoke") || (request.Action == "refresh" && (request.SessionID != "" || request.AllSessions)) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid relay request")
	}
	if s.deps == nil || s.deps.Relay == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "managed relay unavailable")
	}
	if request.Action == "revoke" {
		return s.revokeRelay(ctx, client, request)
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.deps.Relay.Authorize(client.ID, s.relayAllowed(e, client))
		lease, err := s.deps.Relay.Issue(client.ID)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "relay access unavailable")
		}
		var ice []netproto.ICEServer
		if s.deps.ICEServers != nil {
			ice = s.deps.ICEServers(client.uniqueID())
		}
		ice = append(ice, netproto.ICEServer{URLs: append([]string(nil), s.cfg.TURN.URIs...), Username: lease.Username, Credential: lease.Password})
		return s.writeMessageInContext(ctx, client, netproto.MsgRelayResponse, netproto.RelayResponse{Action: "refresh", SessionID: client.ID, Generation: lease.Generation, ServerTime: time.Now().UnixMilli(), IssuedAt: lease.IssuedAt, RefreshAt: lease.RefreshAt, ExpiresAt: lease.ExpiresAt, ICEServers: ice})
	})
}
func (s *TCPServer) revokeRelay(ctx context.Context, actor *Client, request netproto.RelayRequest) error {
	actor.mu.Lock()
	allowed := !time.Now().Before(actor.relayRevokeAfter)
	if allowed {
		actor.relayRevokeAfter = time.Now().Add(time.Second)
	}
	actor.mu.Unlock()
	if !allowed {
		return s.sendErrorFor(actor, requestOrigin(ctx), errCodeMalformed, "relay revocation rate limit exceeded")
	}
	var targetID, targetUniqueID string
	changed := false
	// The registry lock serializes revocation with issuance. A shared policy
	// lease pins authority without stopping unrelated realtime policy readers.
	err := s.withRoleSession(ctx, actor, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		if actor.sessionRevoked() {
			return authorization.ErrRoleForbidden
		}
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		targetID = request.SessionID
		if targetID == "" {
			targetID = actor.ID
		}
		target, ok := s.clientByID(targetID)
		if !ok || !target.isAuthed() {
			return authorization.ErrRoleForbidden
		}
		if target.uniqueID() != actor.uniqueID() {
			if !e.Evaluate(actor.userID(), 0, authorization.KickMembers).Allowed || !e.CanManageMember(actor.userID(), target.userID()) || !e.Evaluate(actor.userID(), 0, authorization.ViewConnectionInfo).Allowed {
				return authorization.ErrRoleForbidden
			}
		}
		targetUniqueID = target.uniqueID()
		for _, member := range s.deps.State.ListClients() {
			if member.ClientID == targetID || (request.AllSessions && member.UniqueID == target.uniqueID()) {
				changed = s.deps.Relay.Revoke(member.ClientID) || changed
			}
		}
		return nil
	})
	if err != nil {
		return s.roleError(ctx, actor, err)
	}
	if changed {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.auditInChannels(auditCtx, actor.uniqueID(), "relay_revoked", targetUniqueID, "session="+targetID)
	}
	return s.writeMessageInContext(ctx, actor, netproto.MsgRelayResponse, netproto.RelayResponse{Action: "revoke", SessionID: targetID, ServerTime: time.Now().UnixMilli()})
}
