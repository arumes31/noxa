package server

import (
	"net"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestPrivateCallPinsAuthenticatedParticipantKeys(t *testing.T) {
	env, _ := privateCallsTestEnv(t)
	defer env.stop()
	caller, callerID := dialCallAuthed(t, env, "admin-uid")
	defer func() { _ = caller.Close() }()
	first, firstID := dialCallAuthed(t, env, "user-uid")
	defer func() { _ = first.Close() }()
	second, secondID := dialCallAuthed(t, env, "user-uid")
	defer func() { _ = second.Close() }()
	call := privateCallRequest(t, caller, netproto.CallRequest{Action: "start", Target: "user-uid"})
	if !call.KeyBinding {
		t.Fatal("new call omitted explicit key binding")
	}
	me, _ := call.Participant("admin-uid")
	client, found := env.state.GetClient(me.ClientID)
	if me.ClientID != callerID || !found || me.PublicKey != client.E2EPublicKey {
		t.Fatalf("caller key did not come from its session: %+v", me)
	}
	// Both sessions of the callee ring; nothing is bound until one answers.
	ringing, _ := call.Participant("user-uid")
	if ringing.ClientID != "" || ringing.PublicKey != "" || ringing.State != "ringing" {
		t.Fatalf("callee bound before answering: %+v", ringing)
	}
	for _, session := range []net.Conn{first, second} {
		if got := privateCallRequest(t, session, netproto.CallRequest{Action: "get", ID: call.ID}); got.ID != call.ID {
			t.Fatal("a ringing session could not read the call")
		}
	}
	selected, other, selectedID := second, first, secondID
	accepted := privateCallRequest(t, selected, netproto.CallRequest{Action: "accept", ID: call.ID})
	peer, _ := accepted.Participant("user-uid")
	session, _ := env.state.GetClient(selectedID)
	if peer.ClientID != selectedID || peer.PublicKey != session.E2EPublicKey || peer.State != "accepted" {
		t.Fatalf("answering session was not bound: %+v", peer)
	}
	// The other session sees the call answered elsewhere and cannot act.
	elsewhere, _ := privateCallRequest(t, other, netproto.CallRequest{Action: "get", ID: call.ID}).Participant("user-uid")
	if elsewhere.State != netproto.CallStateElsewhere {
		t.Fatalf("other session view = %+v", elsewhere)
	}
	send(t, other, netproto.MsgCallRequest, netproto.CallRequest{Action: "leave", ID: call.ID})
	if failure := readError(t, other); failure.Code != errCodePermissionDenied {
		t.Fatalf("same-account other session acted on the call: %+v", failure)
	}
	_ = firstID
	replacement, _ := testX25519(t)
	send(t, selected, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(replacement[:])})
	send(t, selected, netproto.MsgPing, netproto.Ping{})
	readOfType(t, selected, netproto.MsgPong)
	current := privateCallRequest(t, caller, netproto.CallRequest{Action: "get", ID: call.ID})
	pinned, _ := current.Participant("user-uid")
	if pinned.PublicKey != peer.PublicKey || pinned.ClientID != peer.ClientID {
		t.Fatal("later key publication changed an existing call binding")
	}
	privateCallRequest(t, caller, netproto.CallRequest{Action: "leave", ID: call.ID})
	next := privateCallRequest(t, selected, netproto.CallRequest{Action: "start", Target: "admin-uid"})
	newCaller, _ := next.Participant("user-uid")
	if newCaller.PublicKey != b64e(replacement[:]) {
		t.Fatal("new call reused the old device key")
	}
}

func TestPrivateCallRejectsUnpublishedDeviceBeforeInviting(t *testing.T) {
	for _, missing := range []string{"caller", "recipient"} {
		t.Run(missing, func(t *testing.T) {
			env, backend := privateCallsTestEnv(t)
			defer env.stop()
			caller, callerID := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = caller.Close() }()
			callee, calleeID := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = callee.Close() }()
			key, _ := testX25519(t)
			publishedID := callerID
			if missing == "caller" {
				publishedID = calleeID
			}
			env.state.SetE2EPublicKey(publishedID, b64e(key[:]))
			send(t, caller, netproto.MsgCallRequest, netproto.CallRequest{Action: "start", Target: "user-uid"})
			failure := readError(t, caller)
			if failure.Code != errCodeUnavailable || !strings.Contains(failure.Message, "encryption key unavailable") {
				t.Fatalf("missing key did not give actionable failure: %+v", failure)
			}
			backend.callMu.Lock()
			count := len(backend.calls)
			backend.callMu.Unlock()
			if count != 0 {
				t.Fatal("call with incomplete binding was persisted")
			}
		})
	}
}
