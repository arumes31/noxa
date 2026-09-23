package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type privateCallTestStore struct {
	*fakeChat
	callMu  sync.Mutex
	calls   map[string]netproto.CallSession
	groupMu sync.Mutex
	group   netproto.Conversation
}

func (f *privateCallTestStore) SavePrivateCall(_ context.Context, call netproto.CallSession) error {
	f.callMu.Lock()
	defer f.callMu.Unlock()
	call.Participants = append([]netproto.CallParticipant(nil), call.Participants...)
	f.calls[call.ID] = call
	return nil
}

func (f *privateCallTestStore) PrivateCallHistory(_ context.Context, uid string) ([]netproto.CallSession, error) {
	f.callMu.Lock()
	defer f.callMu.Unlock()
	var result []netproto.CallSession
	for _, call := range f.calls {
		if _, ok := call.Participant(uid); ok {
			call.Participants = append([]netproto.CallParticipant(nil), call.Participants...)
			result = append(result, call)
		}
	}
	return result, nil
}

func (f *privateCallTestStore) CreateConversation(context.Context, string, string) (netproto.Conversation, error) {
	return netproto.Conversation{}, netproto.ErrConversationInvalid
}

func (f *privateCallTestStore) SendConversation(context.Context, string, netproto.ConversationRequest) (int64, []string, error) {
	return 0, nil, netproto.ErrConversationInvalid
}

func (f *privateCallTestStore) MarkConversationRead(context.Context, string, string, int64) error {
	return netproto.ErrConversationInvalid
}

func (f *privateCallTestStore) ChangeConversation(_ context.Context, uid string, request netproto.ConversationRequest) ([]string, error) {
	f.groupMu.Lock()
	defer f.groupMu.Unlock()
	next, err := netproto.ChangeConversation(f.group, uid, request)
	if err != nil {
		return nil, err
	}
	uids := []string{}
	for _, member := range f.group.Members {
		uids = append(uids, member.UniqueID)
	}
	f.group = next
	return uids, nil
}

func (f *privateCallTestStore) WithConversations(_ context.Context, uid string, request netproto.ConversationRequest, deliver func(netproto.ConversationResult) error) error {
	f.groupMu.Lock()
	defer f.groupMu.Unlock()
	if _, ok := f.group.Member(uid); !ok || request.ID != f.group.ID {
		return netproto.ErrConversationDenied
	}
	return deliver(netproto.ConversationResult{Action: request.Action, Conversations: []netproto.Conversation{f.group}})
}

func (f *privateCallTestStore) StartPrivateGroupCall(ctx context.Context, uid, groupID string, build func(netproto.Conversation) (netproto.CallSession, error)) (netproto.CallSession, error) {
	f.groupMu.Lock()
	defer f.groupMu.Unlock()
	member, ok := f.group.Member(uid)
	if !ok || member.Pending || groupID != f.group.ID {
		return netproto.CallSession{}, netproto.ErrConversationDenied
	}
	call, err := build(f.group)
	if err != nil {
		return call, err
	}
	return call, f.SavePrivateCall(ctx, call)
}

func privateCallsTestEnv(t *testing.T) (*testEnv, *privateCallTestStore) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	chat := &privateCallTestStore{fakeChat: newFakeChat(), calls: map[string]netproto.CallSession{}, group: netproto.Conversation{
		ID: "ce7189ed-d3d5-442b-b147-241ea9cb3fb9", Name: "Private group", Owner: "user-uid", Revision: 1, Epoch: 1,
		Members: []netproto.ConversationMember{{UniqueID: "admin-uid", JoinedEpoch: 1}, {UniqueID: "user-uid", JoinedEpoch: 1}},
	}}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = chat })
	return env, chat
}

func privateCallRequest(t *testing.T, conn net.Conn, request netproto.CallRequest) netproto.CallSession {
	t.Helper()
	send(t, conn, netproto.MsgCallRequest, request)
	var response netproto.CallResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgCallResult), &response); err != nil {
		t.Fatal(err)
	}
	if response.Action != request.Action || response.Call == nil {
		t.Fatalf("call response=%+v", response)
	}
	return *response.Call
}

func assertPrivateCallChannelZero(t *testing.T, env *testEnv, ids ...string) {
	t.Helper()
	for _, id := range ids {
		channel, _, present := env.state.ClientChannelState(id)
		if !present || channel != 0 {
			t.Fatalf("private call changed voice-channel membership: id=%s channel=%d present=%t", id, channel, present)
		}
	}
}

func privateCallSignalPayload(t *testing.T, id string) ([]byte, string) {
	t.Helper()
	body := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{71}, 64))
	payload, err := eventEnvelope(eventPrivateCallSignal, netproto.CallSignal{CallID: id, From: "admin-uid", To: "user-uid", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return payload, body
}

func assertQueuedPrivateSignal(t *testing.T, env *testEnv, recipient *Client, payload []byte, allowed bool) {
	t.Helper()
	if err := env.srv.withRolePolicy(t.Context(), func(ctx context.Context) error {
		env.srv.privateCallsMu.Lock()
		defer env.srv.privateCallsMu.Unlock()
		frame, err := env.srv.roleBroadcastFrame(recipient, payload, ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		if err != nil || (frame != nil) != allowed {
			t.Errorf("queued private signal allowed=%t frame=%v error=%v", allowed, frame, err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCallChannelZeroLifecycleAndSignals(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	if call.ID == "" || call.Caller != "admin-uid" || len(call.Participants) != 2 || call.EndedAt != 0 {
		t.Fatalf("started call=%+v", call)
	}
	ringing := privateCallRequest(t, callee, netproto.CallRequest{Action: "get", ID: call.ID})
	participant, ok := ringing.Participant("user-uid")
	if !ok || participant.State != "ringing" || participant.ClientID != calleeID {
		t.Fatalf("callee did not ring: %+v", ringing)
	}
	assertPrivateCallChannelZero(t, env, callerID, calleeID)
	payload, body := privateCallSignalPayload(t, call.ID)
	recipient, _ := env.srv.clientByID(calleeID)
	assertQueuedPrivateSignal(t, env, recipient, payload, false)
	for _, attempt := range []struct {
		conn   net.Conn
		target string
	}{{caller, "user-uid"}, {callee, "admin-uid"}} {
		send(t, attempt.conn, netproto.MsgCallRequest, netproto.CallRequest{Action: "signal", ID: call.ID, Target: attempt.target, Signal: body})
		if err := readError(t, attempt.conn); err.Code != errCodePermissionDenied && err.Code != errCodeMalformed {
			t.Fatalf("pre-accept signal result=%+v", err)
		}
	}
	accepted := privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	participant, _ = accepted.Participant("user-uid")
	if participant.State != "accepted" || accepted.EndedAt != 0 {
		t.Fatalf("accept result=%+v", accepted)
	}
	assertQueuedPrivateSignal(t, env, recipient, payload, true)
	privateCallRequest(t, caller, netproto.CallRequest{Action: "signal", ID: call.ID, Target: "user-uid", Signal: body})
	var signal netproto.CallSignal
	if err := json.Unmarshal(readEventOfType(t, callee, eventPrivateCallSignal), &signal); err != nil {
		t.Fatal(err)
	}
	if signal.CallID != call.ID || signal.From != "admin-uid" || signal.To != "user-uid" || signal.Body != body {
		t.Fatalf("signal changed: %+v", signal)
	}
	ended := privateCallRequest(t, callee, netproto.CallRequest{Action: "leave", ID: call.ID})
	if ended.EndedAt == 0 {
		t.Fatal("two-party call did not end after leave")
	}
	assertQueuedPrivateSignal(t, env, recipient, payload, false)
	send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "signal", ID: call.ID, Target: "user-uid", Signal: body})
	if err := readError(t, caller); err.Code != errCodePermissionDenied {
		t.Fatalf("signal after leave=%+v", err)
	}
	assertPrivateCallChannelZero(t, env, callerID, calleeID)
}

func TestPrivateCallTrickleSignalsDoNotUseChatWindow(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	// Simulate an operator's deliberately long chat anti-spam window.
	env.srv.chatRate = newChatRateLimiter(5, time.Minute)
	caller, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	_, body := privateCallSignalPayload(t, call.ID)
	// Fifteen peers need descriptions plus several candidate batches. The
	// client paces the shared signaling stream at less than 20 messages/sec.
	for i := 0; i < 40; i++ {
		privateCallRequest(t, caller, netproto.CallRequest{Action: "signal", ID: call.ID, Target: "user-uid", Signal: body})
		readEventOfType(t, callee, eventPrivateCallSignal)
		time.Sleep(55 * time.Millisecond)
	}
}

func TestPrivateCallDeclineAndBusyDoNotMoveChannels(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	for _, attempt := range []struct {
		conn   net.Conn
		target string
	}{{caller, "user-uid"}, {callee, "admin-uid"}} {
		send(t, attempt.conn, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: attempt.target})
		if err := readError(t, attempt.conn); err.Code != errCodeUnavailable {
			t.Fatalf("busy session started another call: %+v", err)
		}
	}
	declined := privateCallRequest(t, callee, netproto.CallRequest{Action: "decline", ID: call.ID})
	participant, _ := declined.Participant("user-uid")
	if participant.State != "declined" || declined.EndedAt == 0 {
		t.Fatalf("decline did not end call: %+v", declined)
	}
	next := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	if next.ID == call.ID {
		t.Fatal("declined call reused its session")
	}
	privateCallRequest(t, caller, netproto.CallRequest{Action: "cancel", ID: next.ID})
	assertPrivateCallChannelZero(t, env, callerID, calleeID)
}

func TestPrivateCallRejectsUnauthenticatedGuestAndDifferentSession(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	for _, kind := range []string{"unauthenticated", "guest", "different session", "revoked session"} {
		t.Run(kind, func(t *testing.T) {
			output := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
			client := &Client{ID: "different-session", Conn: output}
			if kind != "unauthenticated" {
				client.setIdentity("user-uid", "User", 2, false)
			}
			if kind == "guest" {
				client.setIdentity("guest:call", "Guest", 0, false)
			}
			if kind == "revoked session" {
				client.revokeSession()
			}
			request, err := netproto.Encode(netproto.MsgCallRequest, netproto.CallRequest{Action: "accept", ID: call.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := env.srv.handlePrivateCall(t.Context(), client, request); err != nil {
				t.Fatal(err)
			}
			if err := readSessionError(t, output); err.Code != errCodePermissionDenied {
				t.Fatalf("session boundary accepted: %+v", err)
			}
		})
	}
	stillRinging := privateCallRequest(t, callee, netproto.CallRequest{Action: "get", ID: call.ID})
	participant, _ := stillRinging.Participant("user-uid")
	if participant.State != "ringing" {
		t.Fatal("denied session mutated call")
	}
}

func TestPrivateCallKickClearsCallBeforeDisconnectCleanup(t *testing.T) {
	env, backend := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	target, _ := env.srv.clientByID(callerID)
	recipient, _ := env.srv.clientByID(calleeID)
	payload, _ := privateCallSignalPayload(t, call.ID)
	assertQueuedPrivateSignal(t, env, recipient, payload, true)
	if err := env.srv.withExclusiveRolePolicy(t.Context(), func(ctx context.Context) error {
		env.srv.roleMetadataMu.Lock()
		defer env.srv.roleMetadataMu.Unlock()
		pending, err := env.srv.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "user-uid", calleeID, callerID, "Call moderation")
		if pending {
			t.Error("kick cleanup unexpectedly pending")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if target.needsDisconnectCleanup() {
		t.Fatal("test did not exercise already-cleaned disconnect path")
	}
	env.srv.privateCallsMu.Lock()
	ended := env.srv.privateCalls[call.ID]
	callerBinding, calleeBinding := env.srv.privateCallByClient[callerID], env.srv.privateCallByClient[calleeID]
	env.srv.privateCallsMu.Unlock()
	if ended.EndedAt == 0 || callerBinding != "" || calleeBinding != "" {
		t.Fatalf("kick left live call or busy bindings: call=%+v caller=%q callee=%q", ended, callerBinding, calleeBinding)
	}
	assertQueuedPrivateSignal(t, env, recipient, payload, false)
	history, err := backend.PrivateCallHistory(t.Context(), "user-uid")
	if err != nil || len(history) != 1 || history[0].EndedAt == 0 {
		t.Fatalf("kick did not persist call end: %+v %v", history, err)
	}
}

func TestPrivateCallQueuedSignalRejectsRevokedPublisherBeforeCallCleanup(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	payload, _ := privateCallSignalPayload(t, call.ID)
	sender, _ := env.srv.clientByID(callerID)
	recipient, _ := env.srv.clientByID(calleeID)
	assertQueuedPrivateSignal(t, env, recipient, payload, true)
	// Retain the accepted call snapshot to exercise the separate session check,
	// rather than relying solely on disconnect cleanup changing call state.
	sender.revokeSession()
	assertQueuedPrivateSignal(t, env, recipient, payload, false)
}

func TestPrivateGroupRemovalRevokesCallerAndQueuedSignals(t *testing.T) {
	env, backend := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", ConversationID: backend.group.ID})
	privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	if call.ConversationID != backend.group.ID {
		t.Fatal("group call lost group membership context")
	}
	payload, _ := privateCallSignalPayload(t, call.ID)
	recipient, _ := env.srv.clientByID(calleeID)
	assertQueuedPrivateSignal(t, env, recipient, payload, true)
	send(t, callee, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "remove", ID: backend.group.ID, Revision: 1, Target: "admin-uid"})
	readOfType(t, callee, netproto.MsgConversationResult)
	assertQueuedPrivateSignal(t, env, recipient, payload, false)
	env.srv.privateCallsMu.Lock()
	ended, bound := env.srv.privateCalls[call.ID], env.srv.privateCallByClient[callerID]
	env.srv.privateCallsMu.Unlock()
	participant, _ := ended.Participant("admin-uid")
	if participant.State != "left" || ended.EndedAt == 0 || bound != "" {
		t.Fatalf("removed group member remained in call: %+v binding=%q", ended, bound)
	}
	assertPrivateCallChannelZero(t, env, callerID, calleeID)
}
