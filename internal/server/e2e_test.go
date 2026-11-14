// e2e_test.go covers the wave-4b E2EE plumbing: key publish/request, chat
// key distribution and rotation, plaintext rejection, encrypted channel
// round-trip, and ciphertext spooling.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"noxa/internal/authorization"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestMoveCommitsWhenChannelKeyDeliveryIsCancelled(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	env := startTestEnvLogger(t, nil, nil, nil, zap.New(core))
	defer env.stop()
	conn, clientID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	publicKey, _ := testX25519(t)
	env.state.SetE2EPublicKey(clientID, b64e(publicKey[:]))
	env.state.AddChannel(testChannel(1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := env.srv.moveClient(ctx, clientID, 1, clientID); err != nil {
		t.Fatalf("moveClient returned post-commit key-delivery error: %v", err)
	}
	client, ok := env.state.GetClient(clientID)
	if !ok || client.ChannelID != 1 {
		t.Fatalf("client state after move = %+v, want channel 1", client)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("post-move key delivery logs = %d, want exactly 1", len(entries))
	}
	if entries[0].Message != "delivering channel key after move failed" {
		t.Fatalf("post-move key delivery log = %q", entries[0].Message)
	}
	allowedFields := map[string]bool{"client_id": true, "scope": true, "channel_id": true, "error": true}
	for _, field := range entries[0].Context {
		if !allowedFields[field.Key] {
			t.Fatalf("unexpected delivery log field %q", field.Key)
		}
		if field.Key == "error" && field.Interface != nil {
			if err, ok := field.Interface.(error); ok {
				lower := strings.ToLower(err.Error())
				for _, forbidden := range []string{"public", "private", "sealed"} {
					if strings.Contains(lower, forbidden) {
						t.Fatalf("delivery error leaked key material marker %q: %v", forbidden, err)
					}
				}
			}
		}
	}
}

func TestKeyRequestHonorsRequestContext(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	started := make(chan struct{})
	env.auth.getE2EKeyFn = func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	frame, err := netproto.Encode(netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "offline-user"})
	if err != nil {
		t.Fatalf("encode key request: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The source-keyed lookup limiter (fix 1) reads the connection's address.
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()
	done := make(chan error, 1)
	go func() {
		// A member (user id != 0) reaches the durable lookup unrestricted; the
		// guest scope gate short-circuits before it (see TestGuest* below).
		done <- env.srv.handleKeyRequest(ctx, &Client{ID: "requester", UniqueID: "user-uid", UserID: 2, Conn: serverConn}, frame)
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("key lookup did not receive the request context")
	}
	if err := <-done; err != context.Canceled {
		t.Fatalf("handleKeyRequest error = %v, want context.Canceled", err)
	}
}

func TestKeyRequestRateLimitUsesDedicatedNamespace(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.srv.chatRate = newChatRateLimiter(1, time.Hour)
	// Key lookups have their own budget (separate from chat sends).
	env.srv.keyLookupRate = newChatRateLimiter(1, time.Hour)
	if !env.srv.chatRate.allow("user-uid", time.Now()) {
		t.Fatal("could not consume the ordinary chat namespace")
	}
	frame, err := netproto.Encode(netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "offline-user"})
	if err != nil {
		t.Fatalf("encode key request: %v", err)
	}
	request := func() (*netproto.Frame, error) {
		serverConn, clientConn := net.Pipe()
		defer func() { _ = serverConn.Close() }()
		defer func() { _ = clientConn.Close() }()
		done := make(chan error, 1)
		go func() {
			done <- env.srv.handleKeyRequest(context.Background(), &Client{ID: "requester", UniqueID: "user-uid", Conn: serverConn}, frame)
		}()
		response := readFrame(t, clientConn)
		return response, <-done
	}
	response, err := request()
	if err != nil || netproto.MessageType(response.Type) != netproto.MsgKeyResponse {
		t.Fatalf("first key request = %s, err=%v; want key response", netproto.MessageType(response.Type), err)
	}
	response, err = request()
	if err != nil || netproto.MessageType(response.Type) != netproto.MsgError {
		t.Fatalf("second key request = %s, err=%v; want rate-limit error", netproto.MessageType(response.Type), err)
	}
	var responseErr netproto.Error
	if err := netproto.Decode(response, &responseErr); err != nil || responseErr.Code != errCodeMalformed {
		t.Fatalf("rate-limit response = %+v, decode err=%v", responseErr, err)
	}
	var retry struct {
		AfterMS int64 `json:"retry_after_ms"`
	}
	if err := netproto.Decode(response, &retry); err != nil || retry.AfterMS <= 0 || retry.AfterMS > time.Hour.Milliseconds()+1 {
		t.Fatalf("missing or invalid key retry delay: %+v, %v", retry, err)
	}
}

// testX25519 generates a throwaway X25519 keypair for tests.
func testX25519(t *testing.T) (pub, priv [32]byte) {
	t.Helper()
	p, s, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("box.GenerateKey: %v", err)
	}
	return *p, *s
}

// x25519Pub derives the public key for a private key (X25519 with the
// curve base point).
func x25519Pub(priv [32]byte) [32]byte {
	out, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return [32]byte{}
	}
	var pub [32]byte
	copy(pub[:], out)
	return pub
}

func b64e(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// publishKey sends MsgKeyPublish and reads the global-scope ChannelKey that
// follows.
func publishKey(t *testing.T, conn net.Conn, pub [32]byte) netproto.ChannelKey {
	t.Helper()
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(pub[:])})
	f := readOfType(t, conn, netproto.MsgChannelKey)
	var ck netproto.ChannelKey
	if err := netproto.Decode(f, &ck); err != nil {
		t.Fatalf("decode channel key: %v", err)
	}
	return ck
}

// unseal opens a sealed scope key with the recipient's keypair (kept for
// future client-side assertions; sealed delivery is verified end-to-end by
// the client TOFU/e2e tests).
func unseal(t *testing.T, ck netproto.ChannelKey, pub, priv [32]byte) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(ck.SealedKey)
	if err != nil {
		t.Fatalf("sealed key not base64: %v", err)
	}
	key, ok := box.OpenAnonymous(nil, raw, &pub, &priv)
	if !ok {
		t.Fatal("box.OpenAnonymous failed on sealed channel key")
	}
	return key
}

// TestKeyPublishAndRequest verifies the directory: a registered user's
// published key is persisted and resolvable; guests resolve from state.
func TestKeyPublishAndRequest(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	pub, _ := testX25519(t)
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(pub[:])})
	// The publish triggers delivery of the global key; drain it.
	readOfType(t, conn, netproto.MsgChannelKey)

	// Persisted for the registered user.
	if got := env.auth.e2eByUID["user-uid"]; got != b64e(pub[:]) {
		t.Fatalf("persisted key = %q, want %q", got, b64e(pub[:]))
	}

	// Resolvable via KeyRequest from another client.
	conn2, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn2.Close() }()
	send(t, conn2, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "user-uid"})
	f := readOfType(t, conn2, netproto.MsgKeyResponse)
	var resp netproto.KeyResponse
	if err := netproto.Decode(f, &resp); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	if resp.PublicKey != b64e(pub[:]) {
		t.Fatalf("directory key = %q, want %q", resp.PublicKey, b64e(pub[:]))
	}

	// Unknown user → empty key (omitempty drops the field, so use a fresh
	// struct to avoid stale values from the first decode).
	send(t, conn2, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "nobody"})
	f = readOfType(t, conn2, netproto.MsgKeyResponse)
	var resp2 netproto.KeyResponse
	if err := netproto.Decode(f, &resp2); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	if resp2.PublicKey != "" {
		t.Fatalf("unknown user key = %q, want empty", resp2.PublicKey)
	}
}

// addKeySession registers one online session in both the server registry and
// the state manager, so directory-scope checks can resolve it.
func addKeySession(t *testing.T, env *testEnv, clientID, uid string, userID, channelID int64, status, pub string) {
	t.Helper()
	c := &Client{ID: clientID, Conn: newBlockingTCPConn()}
	c.setIdentity(uid, uid, userID, false)
	env.srv.register(c) // under the server lock; a bare map write races the serve goroutine (-race)
	env.state.AddClient(&state.Client{ClientID: clientID, UniqueID: uid, UserID: userID, Nickname: uid})
	if channelID != 0 {
		if err := env.state.MoveClient(clientID, channelID); err != nil {
			t.Fatalf("move %s to channel %d: %v", clientID, channelID, err)
		}
	}
	if status != "" {
		env.state.SetStatus(clientID, status, "")
	}
	if pub != "" {
		env.state.SetE2EPublicKey(clientID, pub)
	}
}

// keyLookup runs one key_request through the handler and returns the decoded
// KeyResponse. It fails if the handler answered with anything but a response,
// which is what a refused guest lookup must still produce (no error oracle).
func keyLookup(t *testing.T, srv *TCPServer, caller *Client, msg netproto.KeyRequest) netproto.KeyResponse {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()
	caller.Conn = serverConn
	frame, err := netproto.Encode(netproto.MsgKeyRequest, msg)
	if err != nil {
		t.Fatalf("encode key request: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.handleKeyRequest(context.Background(), caller, frame) }()
	response := readFrame(t, clientConn)
	if err := <-done; err != nil {
		t.Fatalf("handleKeyRequest: %v", err)
	}
	if netproto.MessageType(response.Type) != netproto.MsgKeyResponse {
		t.Fatalf("response type = %s, want KeyResponse", netproto.MessageType(response.Type))
	}
	var out netproto.KeyResponse
	if err := netproto.Decode(response, &out); err != nil {
		t.Fatalf("decode key response: %v", err)
	}
	return out
}

// TestGuestKeyLookupScopedToVisibleScope covers fix 3: a guest may resolve a
// principal's directory key only when it shares a visible scope with it
// (self, or an online, visible co-member), while a member stays unrestricted.
// A refusal is byte-for-byte a normal empty lookup, so the directory cannot be
// harvested or used to probe which unique IDs exist.
func TestGuestKeyLookupScopedToVisibleScope(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.state.AddChannel(testChannel(1))

	memberKey := b64e(make([]byte, 32))
	env.auth.e2eByUID = map[string]string{
		"member-uid":    memberKey, // visible co-member (user 3)
		"invisible-uid": memberKey, // invisible, admin-only (user 4)
		"offline-uid":   memberKey, // registered but offline (user 5)
	}

	addKeySession(t, env, "member", "member-uid", 3, 1, "", memberKey)
	addKeySession(t, env, "invisible", "invisible-uid", 4, 1, "invisible", memberKey)
	// The guest's own session, so it can read back its just-published key.
	guestKey := b64e(append(make([]byte, 31), 7))
	addKeySession(t, env, "guest", "guest-uid", 0, 1, "", guestKey)

	guest := func() *Client { return &Client{ID: "guest-caller", UniqueID: "guest-uid"} }

	// An unknown user: the benign empty shape every refusal must be identical to.
	unknown := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "nobody"})
	if unknown.PublicKey != "" || len(unknown.Devices) != 0 {
		t.Fatalf("unknown lookup leaked data: %+v", unknown)
	}

	t.Run("self lookup allowed", func(t *testing.T) {
		got := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "guest-uid"})
		if got.PublicKey != guestKey {
			t.Fatalf("self key = %q, want %q", got.PublicKey, guestKey)
		}
	})
	t.Run("co-channel visible member allowed", func(t *testing.T) {
		got := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "member-uid"})
		if got.PublicKey != memberKey {
			t.Fatalf("co-member key = %q, want %q", got.PublicKey, memberKey)
		}
	})
	t.Run("invisible co-member refused without presence leak", func(t *testing.T) {
		got := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "invisible-uid"})
		if got.PublicKey != unknown.PublicKey || len(got.Devices) != len(unknown.Devices) {
			t.Fatalf("invisible lookup = %+v, want identical to unknown %+v", got, unknown)
		}
	})
	t.Run("unshared offline principal refused indistinguishably", func(t *testing.T) {
		got := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "offline-uid"})
		if got.PublicKey != unknown.PublicKey || got.UniqueID != "offline-uid" {
			t.Fatalf("offline lookup = %+v, want empty key like unknown", got)
		}
	})
	t.Run("member resolves arbitrary principal unrestricted", func(t *testing.T) {
		member := &Client{ID: "member-caller", UniqueID: "user-uid", UserID: 2}
		got := keyLookup(t, env.srv, member, netproto.KeyRequest{UniqueID: "offline-uid"})
		if got.PublicKey != memberKey {
			t.Fatalf("member lookup of offline principal = %q, want %q", got.PublicKey, memberKey)
		}
	})
	t.Run("device directory withheld from a device-less guest", func(t *testing.T) {
		got := keyLookup(t, env.srv, guest(), netproto.KeyRequest{UniqueID: "member-uid", Devices: true})
		if got.PublicKey != memberKey {
			t.Fatalf("key = %q, want %q", got.PublicKey, memberKey)
		}
		if len(got.Devices) != 0 {
			t.Fatalf("device directory served to a device-less guest: %+v", got.Devices)
		}
	})
	t.Run("guest with a linked device resolves an unshared principal", func(t *testing.T) {
		// A key-login guest that linked a device is an accountable DM
		// participant, not the at-scale harvester; it is not scope-gated.
		deviceGuest := &Client{ID: "device-guest", UniqueID: "dguest-uid", deviceID: "dguest-dev"}
		got := keyLookup(t, env.srv, deviceGuest, netproto.KeyRequest{UniqueID: "offline-uid"})
		if got.PublicKey != memberKey {
			t.Fatalf("device-guest lookup of unshared principal = %q, want %q (unrestricted)", got.PublicKey, memberKey)
		}
	})
}

// TestKeyLookupRateLimitKeyedBySource covers fix 1: the key-lookup budget is
// keyed on the source address, not the caller's unique id, so a guest cannot
// reset it by reconnecting under a fresh id. Two callers with different unique
// ids but the same source share one budget.
func TestKeyLookupRateLimitKeyedBySource(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.srv.keyLookupRate = newChatRateLimiter(1, time.Hour)

	request := func(uid string) netproto.MessageType {
		serverConn, clientConn := net.Pipe()
		defer func() { _ = serverConn.Close() }()
		defer func() { _ = clientConn.Close() }()
		frame, err := netproto.Encode(netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "target"})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			done <- env.srv.handleKeyRequest(context.Background(), &Client{ID: "c-" + uid, UniqueID: uid, UserID: 2, Conn: serverConn}, frame)
		}()
		response := readFrame(t, clientConn)
		<-done
		return netproto.MessageType(response.Type)
	}
	if got := request("first-uid"); got != netproto.MsgKeyResponse {
		t.Fatalf("first lookup = %s, want key response", got)
	}
	// A different unique id over the same (pipe) source is already out of budget.
	if got := request("second-uid"); got != netproto.MsgError {
		t.Fatalf("second lookup under a fresh unique id = %s, want rate-limit error", got)
	}
}

func TestKeyPublishSkipsUnreadableAutomaticScopes(t *testing.T) {
	for _, test := range []struct {
		name      string
		channelID int64
		wantKey   bool
	}{
		{name: "not in a channel"},
		{name: "channel access without global access", channelID: 1, wantKey: true},
		{name: "unreadable current channel", channelID: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Channels = []authorization.ChannelPolicy{
				{ChannelID: 1, Overrides: []authorization.RoleOverride{
					{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Allow},
				}},
				{ChannelID: 2},
			}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Authority = authority })
			defer env.stop()
			conn, clientID := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			if test.channelID != 0 {
				env.state.AddChannel(testChannel(test.channelID))
				if err := env.state.MoveClient(clientID, test.channelID); err != nil {
					t.Fatal(err)
				}
			}
			pub, priv := testX25519(t)
			send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(pub[:])})
			// A subsequent Pong is a barrier for the publication and every
			// optional key response, without a timing-based absence assertion.
			send(t, conn, netproto.MsgPing, netproto.Ping{})
			keys := 0
			for {
				frame := readFrame(t, conn)
				kind := netproto.MessageType(frame.Type)
				if kind == netproto.MsgPong {
					break
				}
				if kind == netproto.MsgError {
					t.Fatalf("publishing own key returned an unsolicited error: %s", frame.Payload)
				}
				if kind != netproto.MsgChannelKey {
					continue
				}
				var key netproto.ChannelKey
				if err := netproto.Decode(frame, &key); err != nil {
					t.Fatal(err)
				}
				if !test.wantKey || key.ChannelID != test.channelID {
					t.Fatalf("received unauthorized scope key for channel %d", key.ChannelID)
				}
				if len(unseal(t, key, pub, priv)) != 32 {
					t.Fatal("channel key did not decrypt")
				}
				keys++
			}
			if test.wantKey && keys != 1 {
				t.Fatalf("received %d allowed channel keys, want 1", keys)
			}
			if got := env.auth.e2eByUID["admin-uid"]; got != b64e(pub[:]) {
				t.Fatal("own public key was not persisted")
			}
			send(t, conn, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 0, KeyIDs: []uint32{1}})
			if response := readError(t, conn); response.Code != errCodePermissionDenied || response.OriginType != uint16(netproto.MsgChatKeyRequest) {
				t.Fatalf("explicit global key request was not denied: %+v", response)
			}
		})
	}
}

func TestKeyPublishRejectsInvalidX25519WithoutMutation(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	conn, clientID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "malformed base64", key: "%%%"},
		{name: "zero decoded bytes", key: b64e(nil)},
		{name: "31 decoded bytes", key: b64e(make([]byte, 31))},
		{name: "33 decoded bytes", key: b64e(make([]byte, 33))},
	} {
		t.Run(test.name, func(t *testing.T) {
			send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: test.key})
			frame := readOfType(t, conn, netproto.MsgError)
			var response netproto.Error
			if err := netproto.Decode(frame, &response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if response.Code != errCodeMalformed {
				t.Fatalf("error code = %d, want malformed (%d)", response.Code, errCodeMalformed)
			}
			if _, stored := env.auth.e2eByUID["user-uid"]; stored {
				t.Fatal("invalid public key was persisted")
			}
			client, ok := env.state.GetClient(clientID)
			if !ok {
				t.Fatal("authenticated client is absent from state")
			}
			if client.E2EPublicKey != "" {
				t.Fatalf("invalid public key mutated state to %q", client.E2EPublicKey)
			}
		})
	}
}

// TestChannelKeyRotationOnLeave verifies members get a sealed key on join,
// and a leave rotates the key: remaining members receive a bumped key id and
// sends with the stale key are rejected.
func TestChannelKeyRotationOnLeave(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = alice.Close() }()
	bob, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()

	apub, _ := testX25519(t)
	bpub, _ := testX25519(t)
	publishKey(t, alice, apub)
	publishKey(t, bob, bpub)

	env.state.AddChannel(testChannel(1))
	send(t, alice, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	ckAlice1 := readChannelKeyFor(t, alice, 1)
	send(t, bob, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	ckBob1 := readChannelKeyFor(t, bob, 1)

	if ckAlice1.KeyID != ckBob1.KeyID {
		t.Fatalf("same generation expected: alice %d, bob %d", ckAlice1.KeyID, ckBob1.KeyID)
	}
	if ckAlice1.SealedKey == ckBob1.SealedKey {
		t.Fatal("sealed keys must differ per member (sealed to different pubkeys)")
	}

	// Bob leaves; alice must receive a bumped generation.
	_ = bob.Close()
	ckAlice2 := readChannelKeyFor(t, alice, 1)
	if ckAlice2.KeyID != ckAlice1.KeyID+1 {
		t.Fatalf("rotated key id = %d, want %d", ckAlice2.KeyID, ckAlice1.KeyID+1)
	}

	// A send with the stale key id is rejected.
	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{
		ChannelID: "1", Text: b64e([]byte("ciphertext")), Enc: true, KeyID: ckAlice1.KeyID,
	})
	f := readOfType(t, alice, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Message == "" || e.Message[:5] != "stale" {
		t.Fatalf("error = %q, want stale-key rejection", e.Message)
	}
}

// sealScopeTest seals plaintext the same way a client would (nonce‖secretbox).
func sealScopeTest(t *testing.T, key [32]byte, plain string) string {
	t.Helper()
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return base64.StdEncoding.EncodeToString(append(nonce[:], secretbox.Seal(nil, []byte(plain), &nonce, &key)...))
}

// openScopeTest opens a scope-sealed body the way a client would.
func openScopeTest(t *testing.T, key [32]byte, blobB64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil || len(raw) < 24 {
		t.Fatalf("scope ciphertext is not base64 nonce||box: %q", blobB64)
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, &key)
	if !ok {
		t.Fatal("scope decryption failed")
	}
	return string(plain)
}

// unsealScopeKey opens one sealed generation from a piggybacked key bundle.
func unsealScopeKey(t *testing.T, ck netproto.ChannelKey, pub, priv [32]byte) [32]byte {
	t.Helper()
	var key [32]byte
	copy(key[:], unseal(t, ck, pub, priv))
	return key
}

// TestEncryptedChannelChatRoundTrip verifies an encrypted channel message is
// fanned out as ciphertext: the server decrypts it for moderation/storage
// but relays the ORIGINAL ciphertext to members.
func TestEncryptedChannelChatRoundTrip(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = alice.Close() }()
	bob, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()

	apub, _ := testX25519(t)
	bpub, bpriv := testX25519(t)
	publishKey(t, alice, apub)
	publishKey(t, bob, bpub)

	env.state.AddChannel(testChannel(1))
	send(t, alice, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	readChannelKeyFor(t, alice, 1)
	send(t, bob, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	ckBob := readChannelKeyFor(t, bob, 1)

	// Bob unseals his channel key and sends a properly sealed message.
	var scopeKey [32]byte
	copy(scopeKey[:], unseal(t, ckBob, bpub, bpriv))
	plaintext := "meet at eight"
	ciphertext := sealScopeTest(t, scopeKey, plaintext)
	send(t, bob, netproto.MsgChatSend, netproto.ChatSend{
		ChannelID: "1", Text: ciphertext, Enc: true, KeyID: ckBob.KeyID,
	})

	data := readEventOfType(t, alice, eventChat)
	var chat netproto.ChatBroadcast
	if err := json.Unmarshal(data, &chat); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if !chat.Enc || chat.KeyID != ckBob.KeyID {
		t.Fatalf("chat enc/key = %v/%d, want true/%d", chat.Enc, chat.KeyID, ckBob.KeyID)
	}
	if chat.Text != ciphertext {
		t.Fatalf("server altered the relayed ciphertext: %q != %q", chat.Text, ciphertext)
	}
	if chat.ID == 0 {
		t.Fatal("chat message was not stored (missing id)")
	}
	if chat.E2E {
		t.Fatal("channel chat must not be marked E2E (server-held keys)")
	}
}

// TestE2EDMSpoolCiphertext verifies an encrypted DM to an offline user is
// spooled as ciphertext with the sender's unique ID, and delivered marked
// E2E on the recipient's next login.
func TestE2EDMSpoolCiphertext(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	alice, _ := dialAuthed(t, env.addr, "admin-uid")
	apub, _ := testX25519(t)
	publishKey(t, alice, apub)

	ciphertext := b64e([]byte("sealed-for-bob"))
	send(t, alice, netproto.MsgChatSend, netproto.ChatSend{
		ToUniqueID: "user-uid", Text: ciphertext, Enc: true,
	})

	// Spooled as ciphertext with the sender's unique ID (the server processes
	// the frame asynchronously).
	var entry spooledEntry
	waitFor(t, "encrypted direct message to be spooled", func() bool {
		env.spool.mu.Lock()
		defer env.spool.mu.Unlock()
		if len(env.spool.pending) == 1 {
			entry = env.spool.pending[0]
			return true
		}
		return false
	})
	if entry.Message != ciphertext || entry.FromUniqueID != "admin-uid" {
		t.Fatalf("spooled = %+v, want ciphertext + admin-uid", entry)
	}
	_ = alice.Close()

	// Bob logs in: the offline delivery is marked Enc + E2E with the sender's
	// unique ID so the client can fetch the public key.
	bob, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = bob.Close() }()
	data := readEventOfType(t, bob, eventChat)
	var chat netproto.ChatBroadcast
	if err := json.Unmarshal(data, &chat); err != nil {
		t.Fatalf("unmarshal chat: %v", err)
	}
	if !chat.Offline || !chat.Enc || !chat.E2E || chat.FromUniqueID != "admin-uid" || chat.Text != ciphertext {
		t.Fatalf("delivered spooled chat = %+v", chat)
	}
}

// testKeyManager builds a standalone key manager over a fresh fake store and
// KEK ring, for the tests that need to reason about generations directly.
func testKeyManager(t *testing.T, st ScopeKeyStore) (*chatKeyManager, ScopeKeyStore, *chatcrypto.KEKRing) {
	t.Helper()
	if st == nil {
		st = newFakeScopeKeys()
	}
	kek, err := chatcrypto.LoadKEKRing(filepath.Join(t.TempDir(), "kek.ring"), "", true)
	if err != nil {
		t.Fatalf("load kek ring: %v", err)
	}
	return newChatKeyManager(st, kek, testLogger()), st, kek
}

// TestScopeKeyNotMintedByChatSend is the disk-exhaustion guard: a send naming
// a channel the client is not in must be refused BEFORE anything can mint,
// because minting from an attacker-supplied scope id fills the disk (91).
func TestScopeKeyNotMintedByChatSend(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	pub, _ := testX25519(t)
	publishKey(t, conn, pub)

	fsk, ok := env.deps.ScopeKeys.(*fakeScopeKeys)
	if !ok {
		t.Fatalf("scope key store is %T, want the fake", env.deps.ScopeKeys)
	}
	before := fsk.insertCount()

	send(t, conn, netproto.MsgChatSend, netproto.ChatSend{
		ChannelID: "999999", Text: b64e([]byte("ciphertext")), Enc: true, KeyID: 1,
	})
	f := readOfType(t, conn, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Code != errCodePermissionDenied {
		t.Fatalf("error = %d %q, want permission denied", e.Code, e.Message)
	}
	if got := fsk.insertCount(); got != before {
		t.Fatalf("a rejected send minted %d generations", got-before)
	}

	// The same holds through the plaintext escape hatch, which skips every
	// key check and would otherwise reach EnsureScope.
	send(t, conn, netproto.MsgChatSend, netproto.ChatSend{ChannelID: "999998", Text: "plain"})
	f = readOfType(t, conn, netproto.MsgError)
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Code != errCodePermissionDenied {
		t.Fatalf("plaintext hatch: error = %d %q, want permission denied", e.Code, e.Message)
	}
	if got := fsk.insertCount(); got != before {
		t.Fatalf("the plaintext hatch minted %d generations", got-before)
	}
}

// TestRotateOnUnloadedScopeDoesNotCollide verifies generation ids come from
// the database sequence, never from RAM: a manager that never loaded a scope
// must not re-mint a colliding id with different key material.
func TestRotateOnUnloadedScopeDoesNotCollide(t *testing.T) {
	ctx := t.Context()
	first, st, kek := testKeyManager(t, nil)

	gen1, key1, err := first.EnsureScope(ctx, 7)
	if err != nil {
		t.Fatalf("EnsureScope: %v", err)
	}
	if gen1 != 1 {
		t.Fatalf("first generation = %d, want 1", gen1)
	}

	// A fresh manager over the same store has no memory of scope 7 at all.
	cold := newChatKeyManager(st, kek, testLogger())
	gen2, key2, err := cold.rotate(ctx, 7)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if gen2 != 2 {
		t.Fatalf("rotated generation = %d, want 2 from the sequence", gen2)
	}
	if key1 == key2 {
		t.Fatal("rotation reused the previous key material")
	}
	recovered, err := cold.at(ctx, 7, gen1)
	if err != nil {
		t.Fatalf("at(gen 1): %v", err)
	}
	if recovered != key1 {
		t.Fatal("rotation overwrote generation 1's key material")
	}
}

// TestHistorySurvivesRestart verifies a restart recovers identical key bytes,
// so ciphertext written before it still opens.
func TestHistorySurvivesRestart(t *testing.T) {
	ctx := t.Context()
	before, st, kek := testKeyManager(t, nil)

	gen, key, err := before.EnsureScope(ctx, 3)
	if err != nil {
		t.Fatalf("EnsureScope: %v", err)
	}
	sealedGen, blob, err := before.seal(ctx, 3, "survives the restart")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealedGen != gen {
		t.Fatalf("sealed under %d, want %d", sealedGen, gen)
	}

	after := newChatKeyManager(st, kek, testLogger())
	recovered, err := after.at(ctx, 3, gen)
	if err != nil {
		t.Fatalf("at after restart: %v", err)
	}
	if recovered != key {
		t.Fatal("the restarted manager recovered different key material")
	}
	plain, err := after.open(ctx, 3, gen, blob)
	if err != nil {
		t.Fatalf("open after restart: %v", err)
	}
	if plain != "survives the restart" {
		t.Fatalf("opened %q", plain)
	}
}

// TestRotationCoalescing verifies leaves inside chat_key_rotate_min_seconds
// produce exactly ONE new generation: a client flapping on a bad link would
// otherwise mint one persisted generation per reconnect.
func TestRotationCoalescing(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) { c.ChatKeyRotateMinSecs = 1 })
	defer env.stop()
	ctx := t.Context()

	if _, _, err := env.srv.chatKeys.EnsureScope(ctx, 4); err != nil {
		t.Fatalf("EnsureScope: %v", err)
	}
	fsk := env.deps.ScopeKeys.(*fakeScopeKeys)
	before := fsk.countFor(4)
	var callbacks []func()
	env.srv.rotationAfter = func(delay time.Duration, callback func()) {
		if delay != time.Second {
			t.Fatalf("rotation delay = %v, want 1s", delay)
		}
		callbacks = append(callbacks, callback)
	}

	for i := 0; i < 10; i++ {
		env.srv.rotateScopeKey(ctx, 4)
	}
	if len(callbacks) != 1 {
		t.Fatalf("scheduled callbacks = %d, want 1", len(callbacks))
	}
	callbacks[0]()
	if got := fsk.countFor(4) - before; got != 1 {
		t.Fatalf("ten leaves in one window produced %d generations, want 1", got)
	}
	env.srv.rotMu.Lock()
	pending := env.srv.rotPending[4]
	env.srv.rotMu.Unlock()
	if pending {
		t.Fatal("rotation remained pending after its callback fired")
	}

	env.srv.rotateScopeKey(ctx, 4)
	if len(callbacks) != 2 {
		t.Fatalf("scheduled callbacks after completion = %d, want 2", len(callbacks))
	}
}

// TestChatKeyRequestRejectsEmptyIDs verifies the cheap-to-send request is
// shape-checked before it costs a seal.
func TestChatKeyRequestRejectsEmptyIDs(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	pub, _ := testX25519(t)
	publishKey(t, conn, pub)

	send(t, conn, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 0})
	f := readOfType(t, conn, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Code != errCodeMalformed {
		t.Fatalf("empty key_ids: error = %d, want malformed", e.Code)
	}

	over := make([]uint32, maxKeysPerResponse+1)
	send(t, conn, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 0, KeyIDs: over})
	f = readOfType(t, conn, netproto.MsgError)
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if e.Code != errCodeMalformed {
		t.Fatalf("oversized key_ids: error = %d, want malformed", e.Code)
	}
}

// TestChatKeyRequestRateLimited verifies the handler runs through the same
// per-user bucket as a send: it is cheap to call and expensive to serve.
func TestChatKeyRequestRateLimited(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) {
		c.ChatRateMsgs = 2
		c.ChatRateWindowSeconds = 30
	})
	defer env.stop()

	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	pub, _ := testX25519(t)
	publishKey(t, conn, pub)

	for i := 0; i < 2; i++ {
		send(t, conn, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 0, KeyIDs: []uint32{1}})
		readOfType(t, conn, netproto.MsgChatKeyBundle)
	}
	send(t, conn, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 0, KeyIDs: []uint32{1}})
	f := readOfType(t, conn, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(e.Message, "rate limit") {
		t.Fatalf("error = %q, want a rate-limit rejection", e.Message)
	}
}

// readChannelKeyFor reads ChannelKey frames until one for the given scope
// arrives (the global key may arrive first).
func readChannelKeyFor(t *testing.T, conn net.Conn, scope int64) netproto.ChannelKey {
	t.Helper()
	for i := 0; i < 5; i++ {
		f := readOfType(t, conn, netproto.MsgChannelKey)
		var ck netproto.ChannelKey
		if err := netproto.Decode(f, &ck); err != nil {
			t.Fatalf("decode channel key: %v", err)
		}
		if ck.ChannelID == scope {
			return ck
		}
	}
	t.Fatalf("no ChannelKey for scope %d", scope)
	return netproto.ChannelKey{}
}

// testChannel returns a minimal permanent channel for state registration.
func testChannel(id int64) *state.Channel {
	return &state.Channel{ChannelID: id, Name: "test", ChannelType: 2}
}
