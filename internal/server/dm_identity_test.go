package server

import (
	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
	"testing"
)

func TestSignedDirectoryRecordRequiresAuthenticatedSigner(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	public, private, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: public, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil || !response.OK {
		t.Fatal("authentication failed", err)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	pub, _ := testX25519(t)
	signer, _ := auth.LoadPrivateKey(private)
	record, err := e2ee.SignKeyRecord("user-uid", pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: record})
	readOfType(t, conn, netproto.MsgChannelKey)
	if got, _ := env.srv.senderDMKey(mustServerClient(t, env, response.ClientID)); got != record {
		t.Fatal("signed sender binding discarded")
	}
	env.auth.mu.Lock()
	stored := env.auth.e2eByUID["user-uid"]
	env.auth.mu.Unlock()
	if stored != record {
		t.Fatal("signature not persisted")
	}
	// Scope-key sealing retains the raw X25519 key, never the signed envelope.
	if sc, ok := env.state.GetClient(response.ClientID); !ok || sc.E2EPublicKey != b64e(pub[:]) {
		t.Fatal("scope key compatibility broken")
	}
	_, otherPrivate, _ := auth.GenerateIdentityKeyPair()
	otherSigner, _ := auth.LoadPrivateKey(otherPrivate)
	forged, _ := e2ee.SignKeyRecord("user-uid", pub, otherSigner)
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: forged})
	if failure := readError(t, conn); failure.Code != errCodePermissionDenied {
		t.Fatal(failure)
	}
	send(t, conn, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "user-uid"})
	var directory netproto.KeyResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgKeyResponse), &directory); err != nil || directory.PublicKey != record {
		t.Fatal("directory changed after rejected signer", err)
	}
}
func mustServerClient(t *testing.T, env *testEnv, id string) *Client {
	t.Helper()
	c, ok := env.srv.clientByID(id)
	if !ok {
		t.Fatal("client missing")
	}
	return c
}

func TestLegacyDMRejectsAmbiguousDestinationBeforePresenceLookup(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	sender, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = sender.Close() }()
	receiver, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = receiver.Close() }()
	for _, target := range []string{id, "missing-client"} {
		send(t, sender, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", ToClientID: target, Enc: true, Text: "ciphertext"})
		if failure := readError(t, sender); failure.Code != errCodeMalformed || failure.RecipientKeyStale {
			t.Fatal(failure)
		}
	}
}
