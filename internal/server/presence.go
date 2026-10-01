// presence.go implements the wave-8b presence and social handlers: presence
// status (307-309), pokes (321/322), and the public server-info query (313).
package server

import (
	"container/heap"
	"context"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// Broadcast event types for presence.
const (
	eventStatusChanged = "status_changed"
	eventPoke          = "poke"
)

// validStatuses are the accepted presence values ("" = online). "invisible"
// (381) is admin-only (enforced in handleSetStatus).
var validStatuses = map[string]bool{"": true, "online": true, "away": true, "busy": true, "invisible": true}

// pokeCooldown is the per (caller, target) poke rate limit (322).
const pokeCooldown = 30 * time.Second

// maxPokeEntries bounds retained caller-to-target cooldowns. A full tracker
// fails closed for new pairs, rather than allowing a short burst of unique
// pairs to consume unbounded memory or make every poke scan the whole map.
const maxPokeEntries = 4096

// maxStatusMessage bounds the free-form status/poke text.
const maxStatusMessage = 200

// pokeTracker records the last poke time per caller→target pair. Its zero
// value is ready for use so every TCP server owns an independent tracker.
type pokeTracker struct {
	mu      sync.Mutex
	entries map[string]time.Time
	expiry  pokeExpiryHeap
}

type pokeExpiry struct {
	key string
	at  time.Time
}

type pokeExpiryHeap []pokeExpiry

func (h pokeExpiryHeap) Len() int           { return len(h) }
func (h pokeExpiryHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h pokeExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *pokeExpiryHeap) Push(value any) {
	*h = append(*h, value.(pokeExpiry))
}

func (h *pokeExpiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

// allow admits key when it is outside the cooldown. Expiry pruning only
// touches elapsed heap heads, making an admission O(log n) instead of
// scanning every active caller-to-target pair.
func (t *pokeTracker) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]time.Time)
	}
	for t.expiry.Len() > 0 && !t.expiry[0].at.After(now) {
		expired := heap.Pop(&t.expiry).(pokeExpiry)
		// The equality guard keeps pruning correct if a future tracker change
		// ever renews a key and leaves an older heap item behind.
		if expiry, ok := t.entries[expired.key]; ok && expiry.Equal(expired.at) {
			delete(t.entries, expired.key)
		}
	}
	if _, exists := t.entries[key]; exists {
		return false
	}
	if len(t.entries) >= maxPokeEntries {
		return false
	}
	expiresAt := now.Add(pokeCooldown)
	t.entries[key] = expiresAt
	heap.Push(&t.expiry, pokeExpiry{key: key, at: expiresAt})
	return true
}

func (t *pokeTracker) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// handleSetStatus sets the caller's presence status and message, and
// announces the change. Invisible (381) is admin-only; entering it looks
// like a leave to non-admins, leaving it like a join, and further status
// changes while invisible only reach admins.
func (s *TCPServer) handleSetStatus(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.SetStatus
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed set_status: "+err.Error())
	}
	status := strings.ToLower(strings.TrimSpace(msg.Status))
	if !validStatuses[status] {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid status (want online|away|busy|invisible)")
	}
	if status == "online" {
		status = ""
	}
	if len(msg.Message) > maxStatusMessage {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "status message too long")
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		if status == "invisible" && !e.Evaluate(client.userID(), 0, authorization.Administrator).Allowed {
			return s.roleError(ctx, client, authorization.ErrRoleForbidden)
		}
		s.deps.State.SetStatus(client.ID, status, msg.Message)
		current, ok := s.deps.State.GetClient(client.ID)
		if !ok || current.Status != status || current.StatusMessage != msg.Message {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "session presence unavailable")
		}
		s.refreshRolePublishers(e)
		s.broadcastEvent(eventStatusChanged, statusEvent{ClientID: client.ID, Status: status, Message: msg.Message})
		if msg.AckRequested {
			return s.writeCommittedReply(client, netproto.MsgStatusSaved, netproto.StatusSaved{
				ClientID: client.ID, Status: status, Message: msg.Message,
			})
		}
		return nil
	})
}

// statusEvent is the status_changed broadcast payload.
type statusEvent struct {
	ClientID string `json:"client_id"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

// pokeEvent is the poke relay payload.
type pokeEvent struct {
	FromClientID string `json:"from_client_id"`
	FromNickname string `json:"from_nickname"`
	Message      string `json:"message,omitempty"`
}

// handlePoke relays a poke to the target after a poke-permission check and a
// per-target cooldown (322).
func (s *TCPServer) handlePoke(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.Poke
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed poke: "+err.Error())
	}
	if s.deps == nil || s.deps.Broadcast == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "broadcast backend unavailable")
	}
	if len(msg.Message) > maxStatusMessage {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "poke message too long")
	}
	return s.rolePoke(ctx, client, msg)
}

func (s *TCPServer) sendPoke(ctx context.Context, client, target *Client, msg netproto.Poke) error {
	key := client.ID + "→" + target.ID
	if !s.pokes.allow(key, time.Now()) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "poke cooldown: wait before poking this client again")
	}

	payload, err := eventEnvelope(eventPoke, pokeEvent{
		FromClientID: client.ID,
		FromNickname: client.Username,
		Message:      msg.Message,
	})
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "encoding poke failed")
	}
	if err := s.deps.Broadcast.BroadcastToClient(target.ID, payload); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target unreachable")
	}
	s.logger.Debug("poke relayed",
		zap.String("from", client.ID),
		zap.String("to", target.ID),
	)
	if msg.AckRequested {
		return s.writeCommittedReply(client, netproto.MsgPokeAccepted, netproto.PokeAccepted{ClientID: target.ID})
	}
	return nil
}

// handleServerInfoQuery returns public server information (313). Ungated:
// name/version/uptime/counts are not sensitive.
func (s *TCPServer) handleServerInfoQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.ServerInfoQuery{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_info_query: "+err.Error())
	}
	return s.roleServerInfo(ctx, client)
}
