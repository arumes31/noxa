package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type privateCallTestStore struct {
	*fakeChat
	callMu  sync.Mutex
	calls   map[string]netproto.CallSession
	groupMu sync.Mutex
	group   netproto.Conversation
	// failSaves is the number of coming SavePrivateCall calls that fail.
	failSaves atomic.Int32
}

func (f *privateCallTestStore) SavePrivateCall(_ context.Context, call netproto.CallSession) error {
	if f.failSaves.Add(-1) >= 0 {
		return errors.New("store unavailable")
	}
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

func (f *privateCallTestStore) ChangeConversation(_ context.Context, uid string, request netproto.ConversationRequest) ([]string, bool, error) {
	f.groupMu.Lock()
	defer f.groupMu.Unlock()
	next, err := netproto.ChangeConversation(f.group, uid, request)
	if err != nil {
		return nil, false, err
	}
	uids := []string{}
	for _, member := range f.group.Members {
		uids = append(uids, member.UniqueID)
	}
	f.group = next
	return uids, len(next.Members) == 0, nil
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

func privateCallsTestEnv(t *testing.T, beforeStart ...func(*TCPServer)) (*testEnv, *privateCallTestStore) {
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
	env := startTestEnvLogger(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = chat }, nil, beforeStart...)
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

func dialCallAuthed(t *testing.T, env *testEnv, uid string) (net.Conn, string) {
	t.Helper()
	conn, id := dialAuthed(t, env.addr, uid)
	key, _ := testX25519(t)
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(key[:])})
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, conn, netproto.MsgPong)
	return conn, id
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
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
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
	caller, _ := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, _ := dialCallAuthed(t, env, "user-uid")
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
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
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
	caller, _ := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, _ := dialCallAuthed(t, env, "user-uid")
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
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
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
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
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
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
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

type pagedPrivateCallTestStore struct {
	*privateCallTestStore
	readPage func(string, string) ([]netproto.CallSession, error)
}

func (f *pagedPrivateCallTestStore) PrivateCallHistoryBefore(_ context.Context, uid, before string) ([]netproto.CallSession, error) {
	return f.readPage(uid, before)
}
func TestPrivateCallHistoryCursorRoundTrip(t *testing.T) {
	const last = "00000000-0000-4000-8000-000000000050"
	reads := make(chan string, 2)
	env, _ := privateCallsTestEnv(t, func(s *TCPServer) {
		s.deps.Chat = &pagedPrivateCallTestStore{privateCallTestStore: s.deps.Chat.(*privateCallTestStore), readPage: func(uid, before string) ([]netproto.CallSession, error) {
			if uid != "user-uid" {
				return nil, netproto.ErrCallDenied
			}
			reads <- before
			if before != "" {
				return []netproto.CallSession{}, nil
			}
			rows := make([]netproto.CallSession, 50)
			for i := range rows {
				rows[i] = netproto.CallSession{ID: last, Revision: 1, EndedAt: 100, Participants: []netproto.CallParticipant{{UniqueID: uid, State: "missed"}}}
			}
			return rows, nil
		}}
	})
	defer env.stop()
	conn, _ := dialCallAuthed(t, env, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgCallRequest, netproto.CallRequest{Action: "history", BeforeID: "invalid"})
	var failure netproto.Error
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &failure); err != nil || failure.Code != errCodeMalformed {
		t.Fatalf("invalid cursor accepted: %+v %v", failure, err)
	}
	for _, before := range []string{"", last} {
		send(t, conn, netproto.MsgCallRequest, netproto.CallRequest{Action: "history", BeforeID: before})
		var result netproto.CallResult
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgCallResult), &result); err != nil {
			t.Fatal(err)
		}
		if got := <-reads; got != before {
			t.Fatalf("cursor lost: %q", got)
		}
		if before == "" && (len(result.History) != 50 || result.NextBeforeID != last) {
			t.Fatalf("missing next page: %+v", result)
		}
		if before != "" && (len(result.History) != 0 || result.NextBeforeID != "") {
			t.Fatalf("last page repeats: %+v", result)
		}
	}
}

// A callee whose sessions the caller may not see is treated like the same
// account offline: the start gives the same answer with and without a push
// subscription, and the hidden session gets no event of the call.
func TestPrivateCallStartAnswersAlikeForHiddenAndOfflineCallee(t *testing.T) {
	hide := func(t *testing.T, env *testEnv) net.Conn {
		t.Helper()
		callee, calleeID := dialCallAuthed(t, env, "user-uid")
		env.state.SetStatus(calleeID, "invisible", "")
		if member, _ := env.state.GetClient(calleeID); !env.state.PresenceHidden(member) {
			t.Fatal("callee is not presence-hidden")
		}
		return callee
	}
	t.Run("with a push subscription", func(t *testing.T) {
		pushes := &fakePushes{fakeDevices: &fakeDevices{}}
		env, _ := privateCallsTestEnv(t, func(s *TCPServer) {
			s.deps.Devices = pushes
			s.deps.Push = &fakeSender{sent: make(chan sentPush, 64), gone: map[string]bool{}}
		})
		defer env.stop()
		if err := pushes.SavePushSubscription(t.Context(), store.PushSubscription{DeviceID: "phone", Principal: "user-uid", Endpoint: "https://push.invalid/phone"}); err != nil {
			t.Fatal(err)
		}
		caller, _ := dialCallAuthed(t, env, "admin-uid")
		defer func() { _ = caller.Close() }()
		offline := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
		want, found := offline.Participant("user-uid")
		if !found || want.State != "ringing" || want.ClientID != "" {
			t.Fatalf("offline callee = %+v", offline)
		}
		privateCallRequest(t, caller, netproto.CallRequest{Action: "cancel", ID: offline.ID})

		callee := hide(t, env)
		defer func() { _ = callee.Close() }()
		hidden := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
		if got, found := hidden.Participant("user-uid"); !found || got != want {
			t.Fatalf("hidden online callee = %+v, want it like offline %+v", got, want)
		}
		privateCallRequest(t, caller, netproto.CallRequest{Action: "cancel", ID: hidden.ID})
		_ = callee.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		for {
			frame, err := netproto.ReadFrame(callee)
			if err != nil {
				break // nothing more arrived
			}
			if netproto.MessageType(frame.Type) == netproto.MsgEvent {
				if typ, _ := decodeEvent(t, frame); typ == eventPrivateCall {
					t.Fatal("the hidden session received a call event")
				}
			}
		}
	})
	t.Run("callee without Connect", func(t *testing.T) {
		// Refused whether or not the callee is online, also with a phone.
		// The caller is the owner; the callee has @everyone only.
		backend := serverRoleFixture()
		backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
		authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		pushes := &fakePushes{fakeDevices: &fakeDevices{}}
		env, _ := privateCallsTestEnv(t, func(s *TCPServer) {
			s.deps.Authority = authority
			s.deps.Devices = pushes
			s.deps.Push = &fakeSender{sent: make(chan sentPush, 64), gone: map[string]bool{}}
		})
		defer env.stop()
		if err := pushes.SavePushSubscription(t.Context(), store.PushSubscription{DeviceID: "phone", Principal: "admin-uid", Endpoint: "https://push.invalid/phone"}); err != nil {
			t.Fatal(err)
		}
		caller, _ := dialCallAuthed(t, env, "user-uid")
		defer func() { _ = caller.Close() }()
		send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: "admin-uid"})
		want := readError(t, caller)
		callee, _ := dialCallAuthed(t, env, "admin-uid")
		defer func() { _ = callee.Close() }()
		send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: "admin-uid"})
		if got := readError(t, caller); got != want {
			t.Fatalf("online callee without Connect = %+v, want it like offline %+v", got, want)
		}
	})
	t.Run("without a push subscription", func(t *testing.T) {
		env, _ := privateCallsTestEnv(t)
		defer env.stop()
		caller, _ := dialCallAuthed(t, env, "admin-uid")
		defer func() { _ = caller.Close() }()
		send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: "user-uid"})
		want := readError(t, caller)
		callee := hide(t, env)
		defer func() { _ = callee.Close() }()
		send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: "user-uid"})
		if got := readError(t, caller); got != want {
			t.Fatalf("hidden online callee = %+v, want it like offline %+v", got, want)
		}
	})
}

// A call whose end could not be saved stays live in memory until a retry
// saved it; only then is it retired.
func TestPrivateCallExpirySaveFailureRetriesBeforeRetiring(t *testing.T) {
	env, backend := privateCallsTestEnv(t)
	defer env.stop()
	caller, _ := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, _ := dialCallAuthed(t, env, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	state := func() (live netproto.CallSession, found, dirty bool) {
		env.srv.privateCallsMu.Lock()
		defer env.srv.privateCallsMu.Unlock()
		live, found = env.srv.privateCalls[call.ID]
		return live, found, env.srv.privateCallDirty[call.ID]
	}
	stored := func() netproto.CallSession {
		backend.callMu.Lock()
		defer backend.callMu.Unlock()
		return backend.calls[call.ID]
	}
	env.srv.privateCallsMu.Lock()
	ringing := env.srv.privateCalls[call.ID]
	ringing.RingUntil = time.Now().Unix() - 1
	env.srv.privateCalls[call.ID] = ringing
	env.srv.privateCallsMu.Unlock()

	backend.failSaves.Store(1)
	env.srv.expirePrivateCall(call.ID)
	if live, found, dirty := state(); !found || live.EndedAt == 0 || !dirty || stored().EndedAt != 0 {
		t.Fatalf("after a failed save: live=%+v found=%t dirty=%t stored=%+v", live, found, dirty, stored())
	}
	env.srv.retirePrivateCall(call.ID)
	if _, found, _ := state(); !found {
		t.Fatal("call retired before its end was saved")
	}
	// The retry (normally 10 s later) saves the current state.
	env.srv.privateCallsMu.Lock()
	env.srv.saveCallCleanup(env.srv.privateCalls[call.ID])
	env.srv.privateCallsMu.Unlock()
	if _, _, dirty := state(); dirty || stored().EndedAt == 0 {
		t.Fatalf("retry did not save the end: dirty=%t stored=%+v", dirty, stored())
	}
	env.srv.retirePrivateCall(call.ID)
	if _, found, _ := state(); found {
		t.Fatal("saved call was not retired")
	}
}

// A participant whose connection stopped taking writes does not hold the
// call lock: neither a signal on its way to it nor the reply to its own
// request keeps other call actions and disconnect cleanup waiting.
func TestPrivateCallWritesDoNotHoldCallLock(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	callee, calleeID := dialCallAuthed(t, env, "user-uid")
	defer func() { _ = callee.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	privateCallRequest(t, callee, netproto.CallRequest{Action: "accept", ID: call.ID})
	lockFree := func(what string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond) // lets the blocked write be reached
		if !env.srv.privateCallsMu.TryLock() {
			t.Fatalf("call lock held while %s", what)
		}
		env.srv.privateCallsMu.Unlock()
	}

	// A stalled writer: every write to the session waits for wmu.
	recipient, _ := env.srv.clientByID(calleeID)
	if err := recipient.wmu.LockContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	payload, _ := privateCallSignalPayload(t, call.ID)
	delivered := make(chan error, 1)
	go func() { delivered <- env.srv.writeRoleBroadcast(recipient, payload) }()
	lockFree("a signal waits for its recipient's connection")
	recipient.wmu.Unlock()
	if err := <-delivered; err != nil {
		t.Fatal(err)
	}

	requester, _ := env.srv.clientByID(callerID)
	if err := requester.wmu.LockContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "get", ID: call.ID})
	lockFree("a reply waits for the requester's connection")
	requester.wmu.Unlock()
	readOfType(t, caller, netproto.MsgCallResult)
}
