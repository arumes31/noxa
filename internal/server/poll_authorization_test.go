package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestPollModerationChecksDecodedJSONFields(t *testing.T) {
	for _, field := range []string{"question", "option"} {
		t.Run(field, func(t *testing.T) {
			chat := newAuthorizationPollStore()
			env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ChatWordFilter = "forbidden" }, func(d *Deps) { d.Chat = chat })
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			id, key, err := env.srv.chatKeys.EnsureScope(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			poll := netproto.PollDefinition{Question: "Allowed", Options: []string{"A", "B"}, ClosesAt: time.Now().Add(time.Hour).Unix()}
			if field == "question" {
				poll.Question = "forbidden"
			} else {
				poll.Options[0] = "forbidden"
			}
			body, err := netproto.EncodePoll(poll)
			if err != nil {
				t.Fatal(err)
			}
			body = strings.ReplaceAll(body, "forbidden", `\u0066\u006f\u0072\u0062\u0069\u0064\u0064\u0065\u006e`)
			sendEncChat(t, conn, key, id, "", body)
			if result := readError(t, conn); result.Code != errCodeMalformed {
				t.Fatalf("filter bypass: %+v", result)
			}
			if chat.creates.Load() != 0 {
				t.Fatal("filtered poll reached storage")
			}
		})
	}
}

type authorizationPollStore struct {
	*fakeChat
	pollMu  sync.Mutex
	polls   map[int64]bool
	reads   atomic.Int32
	writes  atomic.Int32
	creates atomic.Int32
}

func newAuthorizationPollStore() *authorizationPollStore {
	return &authorizationPollStore{fakeChat: newFakeChat(), polls: map[int64]bool{}}
}

func (f *authorizationPollStore) StoreChatPoll(ctx context.Context, channelID int64, uid, nickname, ciphertext string, keyID uint32, clientMsgID string, _ netproto.PollDefinition) (int64, bool, error) {
	f.creates.Add(1)
	id, inserted, err := f.StoreChatMessage(ctx, channelID, uid, nickname, ciphertext, keyID, 0, clientMsgID)
	if err == nil {
		f.pollMu.Lock()
		f.polls[id] = true
		f.pollMu.Unlock()
	}
	return id, inserted, err
}

func (f *authorizationPollStore) ReadPoll(_ context.Context, id int64, _ string) (netproto.PollState, error) {
	f.reads.Add(1)
	f.pollMu.Lock()
	defer f.pollMu.Unlock()
	if !f.polls[id] {
		return netproto.PollState{}, sql.ErrNoRows
	}
	return netproto.PollState{MessageID: id, Counts: []int{0, 1}, Choices: []int{1}, TotalVoters: 1, Version: 2}, nil
}

func (f *authorizationPollStore) ChangePoll(_ context.Context, id int64, _ string, choices []int, closePoll bool) (netproto.PollState, error) {
	f.writes.Add(1)
	return netproto.PollState{MessageID: id, Counts: []int{0, 1}, Choices: choices, TotalVoters: 1, Closed: closePoll, Version: 3}, nil
}

func pollAuthorizationEnv(t *testing.T, capabilities []authorization.Capability, hidden, missing, deleted, own bool) (*testEnv, *authorizationPollStore) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = capabilities
	if hidden {
		backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	chat := newAuthorizationPollStore()
	if !missing {
		message := &store.ChatMessage{ID: 42, ChannelID: 1, FromUniqueID: "user-uid", BodyEnc: "ciphertext", KeyID: 1, Version: 1}
		if own {
			message.FromUniqueID = "admin-uid"
		}
		if deleted {
			now := time.Now()
			message.DeletedAt = &now
		}
		chat.messages[42] = message
		chat.polls[42] = true
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = chat })
	return env, chat
}

func TestPollAuthorizationBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name, action                                  string
		capabilities                                  []authorization.Capability
		hidden, missing, deleted, own, guest, allowed bool
	}{
		{name: "get allowed", action: "get", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, allowed: true},
		{name: "get no history", action: "get", capabilities: []authorization.Capability{authorization.ViewChannel}},
		{name: "get hidden", action: "get", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, hidden: true},
		{name: "get missing", action: "get", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, missing: true},
		{name: "get deleted", action: "get", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, deleted: true},
		{name: "vote allowed", action: "vote", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}, allowed: true},
		{name: "vote no send", action: "vote", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}},
		{name: "vote hidden", action: "vote", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}, hidden: true},
		{name: "vote guest", action: "vote", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}, guest: true},
		{name: "vote deleted", action: "vote", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}, deleted: true},
		{name: "close own", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}, own: true, allowed: true},
		{name: "close own no send", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel}, own: true},
		{name: "close others denied", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}},
		{name: "close others moderator", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ManageMessages}, allowed: true},
		{name: "close hidden moderator", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ManageMessages}, hidden: true},
		{name: "close guest moderator", action: "close", capabilities: []authorization.Capability{authorization.ViewChannel, authorization.ManageMessages}, guest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, chat := pollAuthorizationEnv(t, tc.capabilities, tc.hidden, tc.missing, tc.deleted, tc.own)
			defer env.stop()
			var conn net.Conn
			if tc.guest {
				var info netproto.AuthResponse
				conn, info = dialGuest(t, env.addr, "guest", "")
				if !info.OK {
					t.Fatal("guest did not authenticate")
				}
			} else {
				conn, _ = dialAuthed(t, env.addr, "admin-uid")
			}
			defer func() { _ = conn.Close() }()
			request := netproto.PollRequest{MessageID: 42, Action: tc.action}
			if tc.action == "vote" {
				request.Choices = []int{1}
			}
			send(t, conn, netproto.MsgPollRequest, request)
			if tc.allowed {
				var result netproto.PollState
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgPollState), &result); err != nil || result.MessageID != 42 || result.Action != tc.action {
					t.Fatalf("poll result=%+v error=%v", result, err)
				}
				wantReads, wantWrites := int32(0), int32(1)
				if tc.action == "get" {
					wantReads, wantWrites = 1, 0
				}
				if chat.reads.Load() != wantReads || chat.writes.Load() != wantWrites {
					t.Fatalf("poll backend reads=%d writes=%d", chat.reads.Load(), chat.writes.Load())
				}
			} else {
				err := readError(t, conn)
				if tc.guest {
					if err.Code != errCodePermissionDenied {
						t.Fatalf("guest refusal=%+v", err)
					}
				} else if err.Code != errCodeNotFound || err.Message != "message not found" {
					t.Fatalf("refusal disclosed message existence or details: %+v", err)
				}
				if chat.reads.Load() != 0 || chat.writes.Load() != 0 {
					t.Fatalf("denied request reached poll backend: reads=%d writes=%d", chat.reads.Load(), chat.writes.Load())
				}
			}
		})
	}
}

func TestPollMalformedRequestsNeverReachStorage(t *testing.T) {
	env, chat := pollAuthorizationEnv(t, []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false, true)
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	for _, request := range []netproto.PollRequest{
		{MessageID: 0, Action: "get"}, {MessageID: 42, Action: "delete"},
		{MessageID: 42, Action: "vote", Choices: make([]int, 11)},
		{MessageID: 42, Action: "get", Choices: []int{0}},
		{MessageID: 42, Action: "close", Choices: []int{0}},
	} {
		send(t, conn, netproto.MsgPollRequest, request)
		if err := readError(t, conn); err.Code != errCodeMalformed {
			t.Fatalf("malformed request=%+v error=%+v", request, err)
		}
	}
	if chat.reads.Load() != 0 || chat.writes.Load() != 0 {
		t.Fatal("malformed request reached poll storage")
	}
}

func TestPollChangedQueuedDeliveryRechecksVisibility(t *testing.T) {
	policy := serverRoleFixture().policy
	policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	allowed, err := authorization.NewRoleEvaluator(policy)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := eventEnvelope(eventPollChanged, map[string]any{"message_id": int64(42), "channel_id": int64(1), "version": uint64(3)})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := eventEnvelope(roleChannelDelivery, roleChannelEvent{ChannelID: 1, Payload: inner})
	if err != nil {
		t.Fatal(err)
	}
	srv := &TCPServer{}
	client := &Client{UserID: 1, UniqueID: "reader"}
	frame, err := srv.roleBroadcastFrame(client, payload, allowed)
	if err != nil || frame == nil {
		t.Fatalf("allowed frame=%v error=%v", frame, err)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(frame.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(envelope.Data, map[string]any{"message_id": float64(42), "channel_id": float64(1), "version": float64(3)}) {
		t.Fatalf("unexpected poll event data: %+v", envelope.Data)
	}
	policy.Revision++
	policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	revoked, err := authorization.NewRoleEvaluator(policy)
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := srv.roleBroadcastFrame(client, payload, revoked); err != nil || frame != nil {
		t.Fatalf("revoked poll event was delivered: frame=%v error=%v", frame, err)
	}
}

func TestPollEncryptedCreationAndEditBoundaries(t *testing.T) {
	chat := newAuthorizationPollStore()
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Chat = chat })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	observer, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = observer.Close() }()
	keyID, key, err := env.srv.chatKeys.EnsureScope(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	definition := netproto.PollDefinition{Question: "Map?", Options: []string{"Forest", "Desert"}, ClosesAt: time.Now().Add(time.Hour).Unix()}
	body, err := netproto.EncodePoll(definition)
	if err != nil {
		t.Fatal(err)
	}
	sendEncChat(t, conn, key, keyID, "", body)
	var created netproto.ChatBroadcast
	if err := json.Unmarshal(readChatFrom(t, conn, "admin"), &created); err != nil {
		t.Fatal(err)
	}
	if chat.creates.Load() != 1 || created.ID == 0 || !created.Enc || created.Text == body {
		t.Fatalf("poll creation was not stored/encrypted: %+v", created)
	}
	stored, err := chat.GetChatMessage(t.Context(), created.ID)
	if err != nil || stored == nil || stored.BodyEnc != created.Text {
		t.Fatal("poll ciphertext was not retained")
	}
	plain, err := env.srv.chatKeys.open(t.Context(), 0, stored.KeyID, stored.BodyEnc)
	if err != nil || plain != body {
		t.Fatalf("stored poll body mismatch: %q %v", plain, err)
	}
	send(t, conn, netproto.MsgPollRequest, netproto.PollRequest{MessageID: created.ID, Action: "vote", Choices: []int{1}})
	readOfType(t, conn, netproto.MsgPollState)
	var notice map[string]any
	if err := json.Unmarshal(readEventOfType(t, observer, eventPollChanged), &notice); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(notice, map[string]any{"message_id": float64(created.ID), "channel_id": float64(0), "version": float64(3)}) {
		t.Fatalf("poll change leaked ballot/identity: %+v", notice)
	}
	send(t, conn, netproto.MsgChatEdit, netproto.ChatEdit{MessageID: created.ID, NewText: sealScopeTest(t, key, "ordinary replacement"), Enc: true, KeyID: keyID})
	if err := readError(t, conn); err.Code != errCodeMalformed {
		t.Fatalf("editing poll into text was allowed: %+v", err)
	}
	sendEncChat(t, conn, key, keyID, "", "ordinary message")
	var ordinary netproto.ChatBroadcast
	if err := json.Unmarshal(readChatFrom(t, conn, "admin"), &ordinary); err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgChatEdit, netproto.ChatEdit{MessageID: ordinary.ID, NewText: sealScopeTest(t, key, body), Enc: true, KeyID: keyID})
	if err := readError(t, conn); err.Code != errCodeMalformed {
		t.Fatalf("editing text into poll was allowed: %+v", err)
	}
	for _, id := range []int64{created.ID, ordinary.ID} {
		message, err := chat.GetChatMessage(t.Context(), id)
		if err != nil || message.Version != 1 || message.EditedAt != nil {
			t.Fatalf("rejected edit mutated message %d: %+v %v", id, message, err)
		}
	}
}
