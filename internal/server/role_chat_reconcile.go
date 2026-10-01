package server

import (
	"context"
	"errors"
	"fmt"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// reconcileRoleChat is the chat portion of the runtime reconciliation hook.
// It must run under Authority's exclusive gate and never re-enter Authority.
// The full cutover hook must also revoke media, file and metadata access.
func (s *TCPServer) reconcileRoleChat(ctx context.Context, before, after *authorization.RoleEvaluator) error {
	if s.deps == nil || s.deps.State == nil || s.chatKeys == nil || !s.chatKeys.configured() {
		return authorization.ErrAuthorizationUnavailable
	}
	for _, member := range s.deps.State.ListClients() {
		client, ok := s.clientByID(member.ClientID)
		if !ok {
			continue
		}
		var revoked []int64
		for _, scope := range s.deps.State.Subscriptions(client.ID) {
			if !after.Evaluate(client.userID(), scope, authorization.ViewChannel).Allowed {
				revoked = append(revoked, scope)
			}
		}
		s.deps.State.Unsubscribe(client.ID, revoked)
	}
	// Include offline members who retained a key. Rotate synchronously: leave
	// coalescing would reuse a compromised generation after the policy change.
	for _, scope := range authorization.RevokedReadScopes(before, after) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := s.chatKeys.current(ctx, scope); errors.Is(err, ErrNoScopeKey) {
			continue // never mint a key for an unused scope
		} else if err != nil {
			return err
		}
		if _, _, err := s.chatKeys.rotate(ctx, scope); err != nil {
			return fmt.Errorf("rotate revoked scope %d: %w", scope, err)
		}
		for _, member := range s.deps.State.ListClients() {
			if scope != 0 && member.ChannelID != scope && !s.deps.State.IsSubscribed(member.ClientID, scope) {
				continue
			}
			client, ok := s.clientByID(member.ClientID)
			if !ok || !after.Evaluate(client.userID(), scope, authorization.ViewChannel).Allowed {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Failed delivery cannot expose the new key. A slow recipient can
			// obtain the current key after reconnecting.
			if err := s.deliverScopeKeyAllowed(ctx, client, scope); err != nil {
				_ = client.Conn.Close()
			}
		}
	}
	// Publish the new visible tree and subscription set before acknowledging the
	// policy. A recipient whose connection cannot accept it must reconnect.
	for _, member := range s.deps.State.ListClients() {
		if err := ctx.Err(); err != nil {
			return err
		}
		client, ok := s.clientByID(member.ClientID)
		if !ok {
			continue
		}
		if err := s.sendRoleSubscriptionState(client, after); err != nil {
			_ = client.Conn.Close()
			continue
		}
		if err := s.writeMessage(client, netproto.MsgSnapshot, buildRoleSnapshot(s.deps.State, after, client.userID(), client.uniqueID())); err != nil {
			_ = client.Conn.Close()
		}
	}
	return nil
}
