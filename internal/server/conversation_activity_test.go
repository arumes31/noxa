package server

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// receiptTestStore keeps shared read receipts on top of the private-group
// fixture, so the server sees a store that supports them.
type receiptTestStore struct {
	*privateCallTestStore
	mu     sync.Mutex
	shared []string // "uid:id:message"
	marked []string
	seen   map[string]int64 // uid -> shared position
}

func (r *receiptTestStore) MarkConversationRead(_ context.Context, uid, id string, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marked = append(r.marked, uid)
	return nil
}

func (r *receiptTestStore) ShareConversationRead(_ context.Context, uid, id string, messageID int64) ([]string, int64, bool, error) {
	r.groupMu.Lock()
	group := r.group
	r.groupMu.Unlock()
	member, found := group.Member(uid)
	if !found || member.Pending || id != group.ID {
		return nil, 0, false, netproto.ErrConversationDenied
	}
	r.mu.Lock()
	r.shared = append(r.shared, uid)
	if r.seen == nil {
		r.seen = map[string]int64{}
	}
	previous := r.seen[uid]
	position := max(previous, messageID)
	r.seen[uid] = position
	r.mu.Unlock()
	var audience []string
	for _, m := range group.Members {
		if m.UniqueID != uid && !m.Pending {
			audience = append(audience, m.UniqueID)
		}
	}
	return audience, position, position > previous, nil
}

// activityTestEnv starts a server whose private group has owner user-uid,
// accepted admin-uid and, when invited, a pending third-uid.
func activityTestEnv(t *testing.T, pendingThird bool, beforeStart ...func(*TCPServer)) (*testEnv, *privateCallTestStore) {
	t.Helper()
	env, chat := privateCallsTestEnv(t, beforeStart...)
	env.auth.passwords["third-uid"] = "pw"
	env.auth.users["third-uid"] = &auth.User{ID: 3, UniqueID: "third-uid", Nickname: "third"}
	if pendingThird {
		chat.groupMu.Lock()
		chat.group.Members = append(chat.group.Members, netproto.ConversationMember{UniqueID: "third-uid", Pending: true})
		chat.groupMu.Unlock()
	}
	return env, chat
}

const activityGroupID = "ce7189ed-d3d5-442b-b147-241ea9cb3fb9"

func groupTyping(t *testing.T, conn net.Conn) {
	t.Helper()
	send(t, conn, netproto.MsgTyping, netproto.Typing{ConversationID: activityGroupID})
}

func TestConversationCapabilityAdvertised(t *testing.T) {
	env, _ := activityTestEnv(t, false)
	defer env.stop()
	conn, response := dialAuthedPlain(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	if !slices.Contains(response.Capabilities, netproto.CapabilityConversationActivity) {
		t.Fatalf("capabilities=%v, want %s", response.Capabilities, netproto.CapabilityConversationActivity)
	}
}

func TestConversationTypingReachesOnlyAcceptedMembers(t *testing.T) {
	env, _ := activityTestEnv(t, true)
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	invited, _ := dialAuthed(t, env.addr, "third-uid")
	defer func() { _ = invited.Close() }()

	groupTyping(t, owner)
	var event netproto.ConversationTypingEvent
	if err := json.Unmarshal(readEventOfType(t, member, eventConversationTyping), &event); err != nil {
		t.Fatal(err)
	}
	if event.ConversationID != activityGroupID || event.UniqueID != "user-uid" || event.Nickname == "" || event.ClientID == "" {
		t.Fatalf("typing event=%+v", event)
	}
	// Never the legacy "typing" event: an older client would file a relay
	// without channel or direct marker under the global chat.
	expectNoEvent(t, member, eventTyping)
	expectNoEvent(t, invited, eventConversationTyping)
	expectNoEvent(t, owner, eventConversationTyping) // not echoed to the sender

	// A pending invitee cannot announce typing to the members either.
	groupTyping(t, invited)
	expectNoEvent(t, owner, eventConversationTyping)
	expectNoEvent(t, member, eventConversationTyping)
}

func TestConversationTypingFromOutsidersAndMalformedFramesIsDropped(t *testing.T) {
	env, _ := activityTestEnv(t, false)
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	outsider, _ := dialAuthed(t, env.addr, "third-uid")
	defer func() { _ = outsider.Close() }()

	groupTyping(t, outsider)
	expectNoEvent(t, owner, eventConversationTyping)
	// Scope fields are exclusive; an ambiguous frame is neither a group nor a
	// channel indicator.
	send(t, outsider, netproto.MsgTyping, netproto.Typing{ConversationID: activityGroupID, ToUniqueID: "user-uid"})
	send(t, outsider, netproto.MsgTyping, netproto.Typing{ConversationID: strings.ToUpper(activityGroupID)})
	expectNoEvent(t, owner, eventConversationTyping)
	expectNoEvent(t, owner, eventTyping)
	// The outsider's connection survives: typing is fire-and-forget.
	send(t, outsider, netproto.MsgPing, netproto.Ping{})
	readOfType(t, outsider, netproto.MsgPong)
}

func TestConversationTypingStopsImmediatelyAfterRemoval(t *testing.T) {
	env, chat := activityTestEnv(t, false)
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()

	groupTyping(t, member)
	readEventOfType(t, owner, eventConversationTyping)

	send(t, owner, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "remove", ID: activityGroupID, Revision: 1, Target: "admin-uid"})
	readOfType(t, owner, netproto.MsgConversationResult)
	chat.groupMu.Lock()
	if _, still := chat.group.Member("admin-uid"); still {
		chat.groupMu.Unlock()
		t.Fatal("remove did not apply")
	}
	chat.groupMu.Unlock()

	// Beyond the per-scope throttle, so only membership can stop these.
	time.Sleep(2100 * time.Millisecond)
	groupTyping(t, member)
	expectNoEvent(t, owner, eventConversationTyping)
	groupTyping(t, owner)
	expectNoEvent(t, member, eventConversationTyping)
}

func TestConversationTypingIsRateLimited(t *testing.T) {
	env, _ := activityTestEnv(t, false, func(srv *TCPServer) {
		srv.conversationSignalRate = newChatRateLimiter(2, time.Hour)
	})
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()

	// The per-scope throttle collapses a burst into one relay.
	for range 5 {
		groupTyping(t, owner)
	}
	readEventOfType(t, member, eventConversationTyping)
	expectNoEvent(t, member, eventConversationTyping)
	// The per-user budget (2 here, two already spent) holds across scopes and
	// after the throttle window: no more relays and no membership lookups.
	time.Sleep(2100 * time.Millisecond)
	groupTyping(t, owner)
	expectNoEvent(t, member, eventConversationTyping)
	if env.srv.conversationSignalRate.allow("user-uid", time.Now()) {
		t.Fatal("budget was not consumed by the throttled frames")
	}
}

func TestConversationReadReceiptsAreSharedOnlyOnRequest(t *testing.T) {
	var store *receiptTestStore
	env, _ := activityTestEnv(t, true, func(srv *TCPServer) {
		store = &receiptTestStore{privateCallTestStore: srv.deps.Chat.(*privateCallTestStore)}
		srv.deps.Chat = store
	})
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	invited, _ := dialAuthed(t, env.addr, "third-uid")
	defer func() { _ = invited.Close() }()

	// An older client (or one with receipts disabled) reads privately.
	send(t, member, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: 7})
	readOfType(t, member, netproto.MsgConversationResult)
	expectNoEvent(t, owner, eventConversationRead)

	send(t, member, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: 9, ShareRead: true})
	readOfType(t, member, netproto.MsgConversationResult)
	var event netproto.ConversationReadEvent
	if err := json.Unmarshal(readEventOfType(t, owner, eventConversationRead), &event); err != nil {
		t.Fatal(err)
	}
	if event != (netproto.ConversationReadEvent{ConversationID: activityGroupID, UniqueID: "admin-uid", MessageID: 9}) {
		t.Fatalf("read event=%+v", event)
	}
	expectNoEvent(t, invited, eventConversationRead)
	expectNoEvent(t, member, eventConversationRead)

	// A repeated or older position is persisted as a no-op and relays
	// nothing: only an advance fans out to the members.
	for _, position := range []int64{9, 8} {
		send(t, member, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: position, ShareRead: true})
		readOfType(t, member, netproto.MsgConversationResult)
	}
	expectNoEvent(t, owner, eventConversationRead)
	send(t, member, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: 11, ShareRead: true})
	readOfType(t, member, netproto.MsgConversationResult)
	if err := json.Unmarshal(readEventOfType(t, owner, eventConversationRead), &event); err != nil || event.MessageID != 11 {
		t.Fatalf("advanced read event=%+v err=%v", event, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !slices.Equal(store.marked, []string{"admin-uid"}) || len(store.shared) != 4 {
		t.Fatalf("private=%v shared=%v", store.marked, store.shared)
	}
}

func TestConversationReadReceiptsRefusePendingAndFallBackWithoutStoreSupport(t *testing.T) {
	var store *receiptTestStore
	env, _ := activityTestEnv(t, true, func(srv *TCPServer) {
		store = &receiptTestStore{privateCallTestStore: srv.deps.Chat.(*privateCallTestStore)}
		srv.deps.Chat = store
	})
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	invited, _ := dialAuthed(t, env.addr, "third-uid")
	defer func() { _ = invited.Close() }()
	send(t, invited, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: 3, ShareRead: true})
	expectDenied(t, invited, "pending receipt")
	expectNoEvent(t, owner, eventConversationRead)

	// A store without receipts keeps the request private and still succeeds.
	env2, _ := activityTestEnv(t, false, func(srv *TCPServer) {
		srv.deps.Chat = &readPositionTestStore{srv.deps.Chat.(*privateCallTestStore)}
	})
	defer env2.stop()
	owner2, _ := dialAuthed(t, env2.addr, "user-uid")
	defer func() { _ = owner2.Close() }()
	member2, _ := dialAuthed(t, env2.addr, "admin-uid")
	defer func() { _ = member2.Close() }()
	send(t, member2, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "mark_read", ID: activityGroupID, ReadMessageID: 3, ShareRead: true})
	readOfType(t, member2, netproto.MsgConversationResult)
	expectNoEvent(t, owner2, eventConversationRead)
}

func TestAttachmentMetadataTokensStripLikeLegacyTokens(t *testing.T) {
	legacy := "[file:0123456789abcdef0123456789abcdef.vcx#a2V5#report.pdf]"
	sized := "[file:0123456789abcdef0123456789abcdef.vcx#b3RoZXI=#nx1;s=48213#report.pdf]"
	voice := "look [file:aa.vcx#a2V5#nx1;s=9000;d=14000;w=AbC-_x#voice-1.weba] here"
	if got := stripAttachmentRefs(legacy); got != "[file:report.pdf]" {
		t.Fatalf("legacy=%q", got)
	}
	if got := stripAttachmentRefs(sized); got != "[file:report.pdf]" {
		t.Fatalf("sized=%q", got)
	}
	if got := stripAttachmentRefs(voice); got != "look [file:voice-1.weba] here" {
		t.Fatalf("voice=%q", got)
	}
	// A display name that merely looks like metadata is not a metadata segment.
	if got := stripAttachmentRefs("[file:aa.vcx#a2V5#plain-name]"); got != "[file:plain-name]" {
		t.Fatalf("plain=%q", got)
	}
}

func TestConversationActivityQueuedBeforeRemovalIsNotDelivered(t *testing.T) {
	srv := &TCPServer{}
	data := []byte(`{"conversation_id":"` + activityGroupID + `","unique_id":"user-uid"}`)
	if ok, err := srv.conversationActivityDeliverable("admin-uid", data); err != nil || !ok {
		t.Fatalf("member delivery=%v err=%v", ok, err)
	}
	now := time.Now()
	srv.conversationActivity.revoke("admin-uid", activityGroupID, now)
	if ok, _ := srv.conversationActivityDeliverable("admin-uid", data); ok {
		t.Fatal("an event queued before the removal reached the removed member")
	}
	if ok, _ := srv.conversationActivityDeliverable("third-uid", data); !ok {
		t.Fatal("revocation leaked to another member")
	}
	if srv.conversationActivity.recent("admin-uid", activityGroupID, now.Add(conversationActivityRevocationWindow+time.Second)) {
		t.Fatal("revocation never expires")
	}
	if ok, _ := srv.conversationActivityDeliverable("admin-uid", []byte(`{}`)); ok {
		t.Fatal("an activity event without a group was delivered")
	}
}
