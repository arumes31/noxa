package server

import (
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// TestServerSealedDirectMessagesRejected pins "DMs are end-to-end only": a
// direct message sealed with a scope key (server-readable) is refused online
// and offline, by unique ID and by client ID, and is never relayed or
// spooled — even when it is valid ciphertext under the current generation.
func TestServerSealedDirectMessagesRejected(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = alice.Close() }()
	apub, apriv := testX25519(t)
	globalKey := publishKey(t, alice, apub)
	key := unsealScopeKey(t, globalKey, apub, apriv)

	rejected := func(label string) {
		t.Helper()
		if got := readError(t, alice); got.Code != errCodeMalformed || !strings.Contains(got.Message, "direct messages must be end-to-end encrypted") {
			t.Fatalf("%s: %+v", label, got)
		}
	}

	// Offline recipient: refused before the spool.
	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "later"), Enc: true, KeyID: globalKey.KeyID})
	rejected("offline server-sealed DM")

	bob, bobID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()
	bpub, _ := testX25519(t)
	publishKey(t, bob, bpub)

	for _, bad := range []netproto.ChatSend{
		{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "valid"), Enc: true, KeyID: globalKey.KeyID},
		{ToClientID: bobID, Text: sealScopeTest(t, key, "by client"), Enc: true, KeyID: globalKey.KeyID},
		{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "stale"), Enc: true, KeyID: globalKey.KeyID + 1},
		{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "bound"), Enc: true, KeyID: globalKey.KeyID, RecipientPublicKey: b64e(bpub[:])},
	} {
		send(t, alice, netproto.MsgChatSend, bad)
		rejected("online server-sealed DM")
	}
	expectNoEvent(t, bob, eventChat)
	env.spool.mu.Lock()
	defer env.spool.mu.Unlock()
	if len(env.spool.pending) != 0 {
		t.Fatalf("server-sealed DM spooled: %+v", env.spool.pending)
	}
}

// TestPlaintextDirectMessagesNeverRelayedOrSpooled pins the hard rule for
// the DM routes too: an unencrypted body is refused online and offline.
func TestPlaintextDirectMessagesNeverRelayedOrSpooled(t *testing.T) {
	const canary = "canary-dm-plain"
	env := startTestEnv(t, nil)
	defer env.stop()
	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = alice.Close() }()

	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: canary})
	if got := readError(t, alice); got.Code != errCodePermissionDenied || !strings.Contains(got.Message, "plaintext chat is not allowed") {
		t.Fatalf("offline plaintext DM: %+v", got)
	}
	bob, bobID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()
	for _, msg := range []netproto.ChatSend{{ToUniqueID: "user-uid", Text: canary}, {ToClientID: bobID, Text: canary}} {
		send(t, alice, netproto.MsgChatSend, msg)
		if got := readError(t, alice); got.Code != errCodePermissionDenied {
			t.Fatalf("online plaintext DM: %+v", got)
		}
	}
	expectNoEvent(t, bob, eventChat)
	env.spool.mu.Lock()
	defer env.spool.mu.Unlock()
	if len(env.spool.pending) != 0 {
		t.Fatalf("plaintext spooled: %+v", env.spool.pending)
	}
}
