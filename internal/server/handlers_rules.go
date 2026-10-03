// handlers_rules.go gates sessions on accepting the current server rules.
package server

import (
	"context"

	"go.uber.org/zap"

	"noxa/internal/netproto"
)

// setRulesPending arms or clears the server-rules gate on the connection
// (215).
func (c *Client) setRulesPending(pending bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rulesPending = pending
}

// rulesBlocked reports whether the session still owes the server rules an
// answer (215).
func (c *Client) rulesBlocked() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rulesPending
}

// sendPendingRules delivers the operator's rules when this session still owes
// them an answer, and arms the gate that keeps the client out of channels and
// chat until it gives one (215).
//
// Guests are asked on EVERY connect and their answer lives only in this
// session: server_rules_acceptance.user_id references users.id, and a guest
// has no such row, so no acceptance can be recorded for them. Asking every
// time is the honest reading of an item that publishes the rules to everyone
// who joins — the alternative, not asking at all, would exempt exactly the
// users the operator knows least about.
func (s *TCPServer) sendPendingRules(ctx context.Context, client *Client, guest bool) {
	if s.deps == nil || s.deps.Rules == nil {
		return
	}
	var (
		text, hash string
		pending    bool
		err        error
	)
	if guest {
		text, hash, err = s.deps.Rules.Text(ctx)
		pending = hash != ""
	} else {
		text, hash, pending, err = s.deps.Rules.Pending(ctx, client.userID())
	}
	if err != nil {
		s.logger.Warn("reading the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	if !pending {
		return
	}
	if err := s.setSessionRulesPending(ctx, client, true); err != nil {
		_ = client.Conn.Close()
		return
	}
	if err := s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{Text: text, Hash: hash}); err != nil {
		s.logger.Warn("sending the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
	}
}

// handleServerRulesAccept records the caller's acceptance of the wording it
// was shown (215). A stale hash is refused with an error frame AND a fresh
// ServerRules frame carrying the text actually in force, so the client
// re-displays instead of silently consenting to words nobody read. An empty
// ServerRules frame is the acknowledgement of a successful accept: the gate
// state stays server-authoritative, so the dialog never has to guess.
func (s *TCPServer) handleServerRulesAccept(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.acceptSessionRules(ctx, client, f)
	})
}

func (s *TCPServer) acceptSessionRules(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ServerRulesAccept
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_rules_accept: "+err.Error())
	}
	if s.deps == nil || s.deps.Rules == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server rules unavailable")
	}
	text, hash, err := s.deps.Rules.Text(ctx)
	if err != nil {
		s.logger.Warn("reading the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server rules unavailable")
	}
	if hash == "" || msg.Hash != hash {
		if err := s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "the server rules changed since they were shown"); err != nil {
			return err
		}
		if err := s.setSessionRulesPending(ctx, client, hash != ""); err != nil {
			return s.roleError(ctx, client, err)
		}
		return s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{Text: text, Hash: hash})
	}
	// A guest has no users row to write the acceptance to, so it stays on the
	// connection (see sendPendingRules).
	if client.userID() != 0 {
		if err := s.deps.Rules.Accept(ctx, client.userID(), msg.Hash); err != nil {
			s.logger.Warn("recording the rules acceptance failed",
				zap.String("client_id", client.ID),
				zap.Error(err),
			)
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recording the acceptance failed")
		}
	}
	if err := s.setSessionRulesPending(ctx, client, false); err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{})
}

// Rules eligibility also participates in media metadata. A stale acceptance
// can re-block a connected session, so it uses the same barrier as movement.
func (s *TCPServer) setSessionRulesPending(ctx context.Context, client *Client, pending bool) error {
	return s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		client.setRulesPending(pending)
		s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		return nil
	})
}
