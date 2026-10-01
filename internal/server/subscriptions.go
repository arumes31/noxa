// subscriptions.go implements role-authorized channel subscriptions (312).
// A client with ViewChannel receives that channel's chat without standing in it.
//
// Key distribution is the whole difficulty. Channel chat is sealed under the
// channel's scope key and the server relays ciphertext, so a subscriber that
// does not hold the generation would be handed bytes it can never open. A
// subscription therefore means exactly one thing for keys: an entitled
// subscriber is treated as a member of that scope by the sealed-key path —
// it receives the current generation on subscribe, every later generation on
// rotation, and archival generations through the normal bundle request. A
// caller that has published no X25519 key is REFUSED outright rather than
// subscribed into a stream of unreadable ciphertext.
//
// Entitlement is re-checked on the relay path, not only at subscribe time, so
// the first message after a role revocation drops the subscription.
package server

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// maxSubscribeTargets caps one ChannelSubscribe request and maxSubscriptions
// caps a client's standing set. Each accepted target costs a persisted scope
// key generation on first use plus one box.SealAnonymous per rotation, so an
// uncapped set is a self-inflicted amplifier.
const (
	maxSubscribeTargets = 64
	maxSubscriptions    = 64
)

// handleChannelSubscribe adds or removes channel subscriptions and answers
// with the authoritative full set. The reply is never a delta: a client that
// applied a delta it half-received would drift from the server's view with
// nothing to correct it.
func (s *TCPServer) handleChannelSubscribe(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChannelSubscribe
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed channel_subscribe: "+err.Error())
	}
	if len(msg.ChannelIDs) == 0 || len(msg.ChannelIDs) > maxSubscribeTargets {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed,
			fmt.Sprintf("channel_ids count must be 1..%d", maxSubscribeTargets))
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	if client.rulesBlocked() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied,
			"accept the server rules before subscribing to channel chat")
	}
	// Same bucket as a chat send: an accepted target seals a key and a
	// dropped one is re-checked on the next relay, so the loop has a cost.
	if s.chatRate != nil && !s.chatRate.allow(client.UniqueID, time.Now()) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "chat rate limit exceeded — slow down")
	}

	if !msg.Subscribe {
		// The channel the caller stands in is implicit, so it survives this
		// unconditionally: it is not in the explicit set to begin with.
		s.deps.State.Unsubscribe(client.ID, msg.ChannelIDs)
		return s.sendSubscriptionState(ctx, client)
	}

	currentChannelID, e2ePublicKey, ok := s.deps.State.ClientChannelState(client.ID)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	if s.chatKeys == nil || !s.chatKeys.configured() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "chat key manager unavailable")
	}
	if e2ePublicKey == "" {
		// Relaying to this client would be ciphertext it provably cannot
		// open, and a silent subscription is worse than none (312).
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied,
			"publish an encryption key before subscribing — a subscriber that cannot be sealed to could not read the channel")
	}

	held := map[int64]bool{}
	for _, id := range s.deps.State.Subscriptions(client.ID) {
		held[id] = true
	}
	return s.subscribeWithRoles(ctx, client, msg.ChannelIDs, held, currentChannelID)
}

// subscribeAllowed reports whether client may subscribe to channelID.
//
// The role check is evaluated in the target channel's context because a
// subscription hands the caller that channel's scope key.
func (s *TCPServer) subscribeAllowed(ctx context.Context, client *Client, channelID int64) bool {
	return s.roleAllowed(ctx, client, channelID, authorization.ViewChannel)
}

// subscriptionSet returns the authoritative set for a client: its explicit
// subscriptions plus the channel it stands in, which is implicit and cannot
// be unsubscribed.
func (s *TCPServer) subscriptionSet(client *Client) []int64 {
	out := []int64{}
	if s.deps == nil || s.deps.State == nil {
		return out
	}
	seen := map[int64]bool{}
	if channelID, _, ok := s.deps.State.ClientChannelState(client.ID); ok && channelID != 0 {
		seen[channelID] = true
		out = append(out, channelID)
	}
	for _, id := range s.deps.State.Subscriptions(client.ID) {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sendSubscriptionState pushes the authoritative set to one client.
func (s *TCPServer) sendSubscriptionState(ctx context.Context, client *Client) error {
	return s.withRoleSession(ctx, client, func(ctx context.Context) error {
		return s.sendRoleSubscriptionStateInContext(ctx, client, ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
	})
}

func (s *TCPServer) sendRoleSubscriptionState(client *Client, e *authorization.RoleEvaluator) error {
	return s.sendRoleSubscriptionStateInContext(context.Background(), client, e)
}

func (s *TCPServer) sendRoleSubscriptionStateInContext(ctx context.Context, client *Client, e *authorization.RoleEvaluator) error {
	ids := slices.DeleteFunc(s.subscriptionSet(client), func(id int64) bool {
		return !e.Evaluate(client.userID(), id, authorization.ViewChannel).Allowed
	})
	return s.writeMessageInContext(ctx, client, netproto.MsgSubscriptionState, netproto.SubscriptionState{ChannelIDs: ids})
}

// channelSubscribers returns the connected clients that receive channelID's
// chat WITHOUT standing in it. Clients that lost the power since subscribing
// are dropped here and told: this is the only place a revocation is observed.
func (s *TCPServer) channelSubscribers(ctx context.Context, channelID int64) []*Client {
	if channelID == globalChatScope || s.deps == nil || s.deps.State == nil {
		return nil
	}
	var out, revoked []*Client
	for _, sc := range s.deps.State.ChannelSubscribers(channelID) {
		if ctx.Err() != nil {
			return nil
		}
		if sc.ChannelID == channelID {
			continue // a member is already served by the channel fan-out
		}
		client, ok := s.clientByID(sc.ClientID)
		if !ok {
			continue
		}
		allowed := s.subscribeAllowed(ctx, client, channelID)
		// Cancellation is not a permission decision. In particular, an expired
		// key fanout must not unsubscribe otherwise authorized readers.
		if ctx.Err() != nil {
			return nil
		}
		if !allowed {
			revoked = append(revoked, client)
			continue
		}
		out = append(out, client)
	}
	for _, c := range revoked {
		if ctx.Err() != nil {
			return nil
		}
		s.deps.State.Unsubscribe(c.ID, []int64{channelID})
		_ = s.sendSubscriptionState(ctx, c)
	}
	return out
}

// broadcastChannelScoped delivers a pre-wrapped event envelope to a channel's
// members and to its subscribers.
func (s *TCPServer) broadcastChannelScoped(ctx context.Context, channelID int64, payload []byte) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	if s.deps.State == nil {
		return
	}
	guarded, err := eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: channelID, Payload: payload})
	if err != nil {
		return
	}
	for _, sc := range s.deps.State.ListClients() {
		if channelID != 0 && sc.ChannelID != channelID && !s.deps.State.IsSubscribed(sc.ClientID, channelID) {
			continue
		}
		if client, ok := s.clientByID(sc.ClientID); ok {
			_ = s.withRoleAccess(ctx, client, channelID, authorization.ViewChannel, func(context.Context) error {
				return s.deps.Broadcast.BroadcastToClient(client.ID, guarded)
			})
		}
	}
}

// Each subscription and its key delivery share a policy revision. Hidden and
// absent targets use the same refusal; no legacy automatic group grant runs.
func (s *TCPServer) subscribeWithRoles(ctx context.Context, client *Client, targets []int64, held map[int64]bool, currentChannelID int64) error {
	refused := false
	for _, id := range targets {
		err := s.withRoleAccess(ctx, client, id, authorization.ViewChannel, func(ctx context.Context) error {
			if id <= 0 {
				return authorization.ErrRoleForbidden
			}
			if _, ok := s.deps.State.GetChannel(id); !ok {
				return authorization.ErrRoleForbidden
			}
			if id == currentChannelID || held[id] {
				return nil
			}
			if len(held) >= maxSubscriptions {
				return authorization.ErrRoleForbidden
			}
			s.deps.State.Subscribe(client.ID, []int64{id})
			if err := s.deliverScopeKey(ctx, client, id); err != nil {
				s.deps.State.Unsubscribe(client.ID, []int64{id})
				return err
			}
			held[id] = true
			return nil
		})
		if err != nil {
			refused = true
		}
	}
	if refused {
		if err := s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "one or more channels are unavailable for subscription"); err != nil {
			return err
		}
	}
	return s.sendSubscriptionState(ctx, client)
}

// pushSubscriptionStateTo re-pushes the authoritative set to the named
// clients, so a drop they did not ask for still reaches them.
func (s *TCPServer) pushSubscriptionStateTo(ctx context.Context, clientIDs []string) {
	for _, id := range clientIDs {
		if client, ok := s.clientByID(id); ok {
			_ = s.sendSubscriptionState(ctx, client)
		}
	}
}
