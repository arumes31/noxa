package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"noxa/internal/netproto"
)

// Private-group activity signals: typing indicators and shared read receipts.
//
// Both are routing metadata the server already holds (who belongs to a group,
// which message ids exist, who read what for unread counts), so neither needs
// or carries message content and neither weakens the per-member end-to-end
// encryption of group messages. Typing is ephemeral: it is relayed and never
// stored. A read receipt is a single shared position per member, advanced only
// while that member's client opts in (ConversationRequest.ShareRead).
//
// Every relay re-reads the authoritative roster under the conversation lock,
// so a member who left or was removed neither sends nor receives a signal from
// the next one on; there is no membership cache to expire.
const (
	eventConversationTyping = "conversation_typing"
	eventConversationRead   = "conversation_read"
)

// conversationActivityRevocationWindow is how long a leave/remove keeps
// suppressing activity events already queued for the departed member. Events
// are queued for milliseconds, so the window only has to outlast the queue;
// a member re-invited inside it merely misses a few seconds of indicators.
const conversationActivityRevocationWindow = 30 * time.Second

// conversationActivityRevocations closes the gap between the roster check at
// relay time and delivery: an event queued just before a leave/remove is
// dropped at delivery instead of reaching the departed member.
type conversationActivityRevocations struct {
	mu      sync.Mutex
	revoked map[string]time.Time // uid + "\x00" + group -> revocation time
}

func (r *conversationActivityRevocations) revoke(uid, group string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked == nil {
		r.revoked = map[string]time.Time{}
	}
	for key, at := range r.revoked {
		if now.Sub(at) > conversationActivityRevocationWindow {
			delete(r.revoked, key)
		}
	}
	r.revoked[conversationAccessKey(uid, group)] = now
}

func (r *conversationActivityRevocations) recent(uid, group string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.revoked[conversationAccessKey(uid, group)]
	return ok && now.Sub(at) <= conversationActivityRevocationWindow
}

// conversationActivityDeliverable is the delivery policy of the two activity
// events (role_delivery.go): addressed by the server, and dropped for a
// recipient that just left or was removed from the group they name.
func (s *TCPServer) conversationActivityDeliverable(recipient string, data []byte) (bool, error) {
	var event struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return false, err
	}
	return event.ConversationID != "" && !s.conversationActivity.recent(recipient, event.ConversationID, time.Now()), nil
}

// conversationReceiptStore is implemented by stores that keep shared read
// receipts. A store without it keeps mark_read private (the older contract).
type conversationReceiptStore interface {
	// ShareConversationRead returns the receipt's audience, the shared
	// position after the update and whether that position advanced.
	ShareConversationRead(ctx context.Context, uid, id string, messageID int64) (audience []string, seen int64, advanced bool, err error)
}

// conversationAudience returns the other accepted members of group when uid is
// itself an accepted member. Pending invitees neither send nor receive.
func (s *TCPServer) conversationAudience(ctx context.Context, uid, group string) ([]string, bool) {
	if uid == "" || s.deps == nil {
		return nil, false
	}
	backend, ok := s.deps.Chat.(conversationStore)
	if !ok {
		return nil, false
	}
	var audience []string
	member := false
	err := backend.WithConversations(ctx, uid, netproto.ConversationRequest{Action: "get", ID: group}, func(result netproto.ConversationResult) error {
		for _, c := range result.Conversations {
			if c.ID != group {
				continue
			}
			for _, m := range c.Members {
				switch {
				case m.Pending:
				case m.UniqueID == uid:
					member = true
				default:
					audience = append(audience, m.UniqueID)
				}
			}
		}
		return nil
	})
	if err != nil || !member {
		return nil, false
	}
	return audience, true
}

// relayToMembers sends one event to every listed member that is online.
func (s *TCPServer) relayToMembers(uids []string, eventType string, data any) error {
	if s.deps == nil || s.deps.Broadcast == nil || len(uids) == 0 {
		return nil
	}
	payload, err := eventEnvelope(eventType, data)
	if err != nil {
		return err
	}
	for _, uid := range uids {
		// Every session: a member's desktop and phones alike.
		for _, target := range s.sessionsOf(uid) {
			_ = s.deps.Broadcast.BroadcastToClient(target.ID, payload)
		}
	}
	return nil
}

// canonicalConversationID reports whether id is the lowercase UUID form the
// store issues; anything else cannot name a group and is not looked up.
func canonicalConversationID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// relayConversationTyping relays a private-group typing indicator. Like every
// typing relay it is fire-and-forget: a frame the sender is not entitled to is
// dropped without an error frame. Two limits apply before the membership
// lookup: the per-scope typing throttle and a per-user budget across all
// groups, so naming many (unknown) groups cannot turn into database load.
func (s *TCPServer) relayConversationTyping(ctx context.Context, client *Client, msg netproto.Typing) error {
	if msg.ChannelID != 0 || msg.ToUniqueID != "" || !canonicalConversationID(msg.ConversationID) {
		return nil
	}
	if !client.isAuthed() || client.userID() <= 0 || client.rulesBlocked() {
		return nil
	}
	uid := client.UniqueID
	now := time.Now()
	if s.conversationSignalRate != nil && !s.conversationSignalRate.allow(uid, now) {
		return nil
	}
	if s.typingRate != nil && !s.typingRate.allow(uid+"\x00group:"+msg.ConversationID, now) {
		return nil
	}
	audience, member := s.conversationAudience(ctx, uid, msg.ConversationID)
	if !member {
		return nil
	}
	return s.relayToMembers(audience, eventConversationTyping, netproto.ConversationTypingEvent{
		ConversationID: msg.ConversationID,
		ClientID:       client.ID,
		UniqueID:       uid,
		Nickname:       client.nickname(),
	})
}

// shareConversationRead advances the caller's private cursor and shared read
// receipt, then tells the other current members. ok=false means the store
// keeps no receipts and the caller falls back to a private mark_read. Only an
// advance is relayed: a repeated or older position changes nothing members
// could see, so it must not fan out (the relay count per member is bounded by
// the number of messages, not by the mark_read rate).
func (s *TCPServer) shareConversationRead(ctx context.Context, backend conversationStore, client *Client, id string, messageID int64) (bool, error) {
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	uid := client.uniqueID()
	if s.deps.State == nil {
		return false, nil
	}
	if member, ok := s.deps.State.GetClient(client.ID); !ok || member.Status == "invisible" {
		return false, nil
	}
	receipts, ok := backend.(conversationReceiptStore)
	if !ok {
		return false, nil
	}
	audience, seen, advanced, err := receipts.ShareConversationRead(ctx, uid, id, messageID)
	if err != nil || !advanced {
		return true, err
	}
	return true, s.relayToMembers(audience, eventConversationRead, netproto.ConversationReadEvent{
		ConversationID: id, UniqueID: uid, ClientID: client.ID, MessageID: seen,
	})
}
