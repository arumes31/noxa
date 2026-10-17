package server

import (
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// TestServerSealedDirectMessageFallback covers the client→server→client
// path for direct messages: a sender without the recipient's end-to-end key
// seals the body with the current global generation. The server verifies the
// ciphertext, relays it marked as server-encrypted (never as E2E) and refuses
// stale generations, non-ciphertext and offline spooling.
func TestServerSealedDirectMessageFallback(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = alice.Close() }()
	apub, apriv := testX25519(t)
	globalKey := publishKey(t, alice, apub)
	key := unsealScopeKey(t, globalKey, apub, apriv)

	// Offline recipient: the fallback is never spooled.
	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "later"), Enc: true, KeyID: globalKey.KeyID})
	if got := readError(t, alice); got.Code != errCodeNotFound || !strings.Contains(got.Message, "end-to-end key") {
		t.Fatalf("offline fallback: %+v", got)
	}
	env.spool.mu.Lock()
	spooled := len(env.spool.pending)
	env.spool.mu.Unlock()
	if spooled != 0 {
		t.Fatalf("server-encrypted DM spooled: %d", spooled)
	}

	bob, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()
	bpub, _ := testX25519(t)
	publishKey(t, bob, bpub)

	for _, bad := range []netproto.ChatSend{
		{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "stale"), Enc: true, KeyID: globalKey.KeyID + 1},
		{ToUniqueID: "user-uid", Text: "bm90IGNpcGhlcnRleHQgYXQgYWxsLCBqdXN0IHBsYWlu", Enc: true, KeyID: globalKey.KeyID},
		{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "bound"), Enc: true, KeyID: globalKey.KeyID, RecipientPublicKey: b64e(bpub[:])},
	} {
		send(t, alice, netproto.MsgChatSend, bad)
		if got := readError(t, alice); got.Code != errCodeMalformed {
			t.Fatalf("invalid fallback %+v accepted: %+v", bad, got)
		}
	}
	expectNoEvent(t, bob, eventChat)

	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: sealScopeTest(t, key, "fallback"), Enc: true, KeyID: globalKey.KeyID})
	var chat netproto.ChatBroadcast
	if err := json.Unmarshal(readEventOfType(t, bob, eventChat), &chat); err != nil {
		t.Fatal(err)
	}
	if !chat.Direct || !chat.Enc || chat.E2E || chat.KeyID != globalKey.KeyID || openScopeTest(t, key, chat.Text) != "fallback" {
		t.Fatalf("fallback relay = %+v", chat)
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
