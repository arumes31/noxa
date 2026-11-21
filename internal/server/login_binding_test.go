package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"testing/synctest"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// loginChallengeOn asks for a login challenge the way a current client does.
func loginChallengeOn(t *testing.T, conn net.Conn, request netproto.Authenticate) []byte {
	t.Helper()
	send(t, conn, netproto.MsgAuthenticate, request)
	var challenge netproto.AuthChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Scheme != netproto.AuthChallengeSchemeV2 || len(challenge.Challenge) != auth.LoginChallengeSize {
		t.Fatalf("challenge = %+v", challenge)
	}
	return challenge.Challenge
}

// loginProofs are the signatures a server must tell apart: the one bound to
// it, and every form that is not.
func loginProofs(t *testing.T, private, binding string, challenge []byte) (bound []byte, refused map[string][]byte) {
	t.Helper()
	sign := func(message []byte) []byte {
		sig, err := auth.SignChallenge(private, message)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	bound = sign(auth.LoginChallengeMessage(binding, challenge))
	return bound, map[string][]byte{
		"raw challenge":         sign(challenge),
		"unbound v1 domain":     sign(append([]byte("noxa-auth-challenge-v1\n"), challenge...)),
		"another certificate":   sign(auth.LoginChallengeMessage("tls-sha256:"+strings.Repeat("0f", 32), challenge)),
		"a web origin":          sign(auth.LoginChallengeMessage(netproto.LoginBindingOrigin(passkeyTestOrigin), challenge)),
		"no binding in v2 form": sign(append([]byte(auth.LoginChallengeDomain+"\n"), challenge...)),
	}
}

// A login proof is accepted only when it names this server: on the account
// path, the key path and as the key proof of a password login.
func TestLoginProofMustBeBoundToThisServer(t *testing.T) {
	// Many refusals from one address: the source lockout is not under test.
	limiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{MaxFailures: 100})
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.LoginLimiter = limiter })
	defer env.stop()
	keys := newLinkTestKeys(t)
	env.auth.users[keys.uniqueID] = &auth.User{ID: 3, UniqueID: keys.uniqueID, Nickname: "member"}
	env.auth.pubkeys[keys.uniqueID] = keys.public

	for _, presentKey := range []bool{false, true} {
		attempt := func(pick func(bound []byte, refused map[string][]byte) []byte) netproto.AuthResponse {
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			challenge := loginChallengeOn(t, conn, netproto.Authenticate{Username: keys.uniqueID, Anonymous: presentKey})
			reply := netproto.AuthSignature{UniqueID: keys.uniqueID, Signature: pick(loginProofs(t, keys.private, loginBindingOf(conn), challenge))}
			if presentKey {
				reply.PublicKey = keys.public
			}
			send(t, conn, netproto.MsgAuthSignature, reply)
			return authReply(t, conn)
		}
		_, forms := loginProofs(t, keys.private, "tls-sha256:"+strings.Repeat("00", 32), make([]byte, auth.LoginChallengeSize))
		for name := range forms {
			if resp := attempt(func(_ []byte, refused map[string][]byte) []byte { return refused[name] }); resp.OK || resp.Reason != "invalid credentials" {
				t.Fatalf("presentKey=%v: a proof over %s = %+v", presentKey, name, resp)
			}
		}
		if resp := attempt(func(bound []byte, _ map[string][]byte) []byte { return bound }); !resp.OK || resp.UniqueID != keys.uniqueID {
			t.Fatalf("presentKey=%v: bound proof = %+v", presentKey, resp)
		}
	}
	if got := env.auth.loginBinding; !netproto.ValidServerBinding(got) {
		t.Fatalf("the account path verified against %q, want this server's certificate binding", got)
	}

	// Password login: only a bound proof makes the presented key a proven one.
	proofKeys := newLinkTestKeys(t)
	proven := func(pick func(bound []byte, refused map[string][]byte) []byte) bool {
		conn := dialRetry(t, env.addr)
		defer func() { _ = conn.Close() }()
		challenge := loginChallengeOn(t, conn, netproto.Authenticate{Username: "user-uid"})
		proof := pick(loginProofs(t, proofKeys.private, loginBindingOf(conn), challenge))
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: proofKeys.public, PublicKeyProof: proof})
		if resp := authReply(t, conn); !resp.OK {
			t.Fatalf("password login: %+v", resp)
		}
		env.auth.mu.Lock()
		defer env.auth.mu.Unlock()
		return env.auth.bindings[len(env.auth.bindings)-1][2] == true
	}
	_, forms := loginProofs(t, proofKeys.private, "tls-sha256:"+strings.Repeat("00", 32), make([]byte, auth.LoginChallengeSize))
	for name := range forms {
		if proven(func(_ []byte, refused map[string][]byte) []byte { return refused[name] }) {
			t.Fatalf("a key proof over %s proved the key", name)
		}
	}
	if !proven(func(bound []byte, _ map[string][]byte) []byte { return bound }) {
		t.Fatal("a bound key proof did not prove the key")
	}
}

// A challenge issued by one server, signed by a client that believes it is
// talking to another, signs nobody in: the client's proof names the server it
// is connected to.
func TestLoginProofForAnotherServerIsRefused(t *testing.T) {
	other := startTestEnv(t, nil)
	defer other.stop()
	target := startTestEnv(t, nil)
	defer target.stop()
	keys := newLinkTestKeys(t)
	for _, env := range []*testEnv{other, target} {
		env.auth.users[keys.uniqueID] = &auth.User{ID: 3, UniqueID: keys.uniqueID, Nickname: "member"}
		env.auth.pubkeys[keys.uniqueID] = keys.public
	}
	toOther := dialRetry(t, other.addr)
	defer func() { _ = toOther.Close() }()
	if loginBindingOf(toOther) == loginBindingOf(dialRetry(t, target.addr)) {
		t.Fatal("the two test servers share an identity")
	}
	for _, presentKey := range []bool{false, true} {
		toTarget := dialRetry(t, target.addr)
		challenge := loginChallengeOn(t, toTarget, netproto.Authenticate{Username: keys.uniqueID, Anonymous: presentKey})
		// The client is connected to the other server and signs for that one.
		signature, err := auth.SignLoginChallenge(keys.private, loginBindingOf(toOther), challenge)
		if err != nil {
			t.Fatal(err)
		}
		reply := netproto.AuthSignature{UniqueID: keys.uniqueID, Signature: signature}
		if presentKey {
			reply.PublicKey = keys.public
		}
		send(t, toTarget, netproto.MsgAuthSignature, reply)
		if resp := authReply(t, toTarget); resp.OK || resp.Reason != "invalid credentials" {
			t.Fatalf("presentKey=%v: a proof for another server = %+v", presentKey, resp)
		}
		_ = toTarget.Close()
	}
}

// A client that does not announce the bound login proof is told to update
// before any challenge is issued, on every path that would sign one.
func TestClientWithoutBoundLoginIsToldToUpdate(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	models := []string{netproto.AuthorizationModelRolesV1}
	for name, request := range map[string]netproto.Authenticate{
		"account key login":   {Username: "user-uid", ClientVersion: "0.6.100"},
		"guest key login":     {Username: "guest-id", Anonymous: true, Nickname: "Guest", ClientVersion: "0.6.100"},
		"password key probe":  {Username: "user-uid", ClientVersion: "0.6.100", ClientFeatures: []string{netproto.ClientFeaturePow, netproto.ClientFeatureResume}},
		"no version reported": {Username: "user-uid"},
	} {
		conn := dialRetry(t, env.addr)
		request.AuthorizationModels = models
		sendFrame(t, conn, netproto.MsgAuthenticate, request)
		resp := authReply(t, conn)
		installed := request.ClientVersion
		if installed == "" {
			installed = "unknown"
		}
		if resp.OK || !strings.HasPrefix(resp.Reason, "client version rejected: installed "+installed+"; required >=") || !strings.HasSuffix(resp.Reason, "; update noXa to connect") {
			t.Fatalf("%s: %+v", name, resp)
		}
		// No challenge is pending: a signature cannot follow the refusal.
		sendFrame(t, conn, netproto.MsgAuthSignature, netproto.AuthSignature{UniqueID: request.Username, Signature: make([]byte, 64)})
		if after := authReply(t, conn); after.OK {
			t.Fatalf("%s: signature after the refusal = %+v", name, after)
		}
		_ = conn.Close()
	}
	// Logins that sign nothing are not affected.
	for name, request := range map[string]netproto.Authenticate{
		"password":        {Username: "user-uid", Password: "pw"},
		"ephemeral guest": {Anonymous: true, Nickname: "Guest"},
	} {
		conn := dialRetry(t, env.addr)
		request.AuthorizationModels = models
		sendFrame(t, conn, netproto.MsgAuthenticate, request)
		if resp := authReply(t, conn); !resp.OK {
			t.Fatalf("%s: %+v", name, resp)
		}
		_ = conn.Close()
	}
}

// Without control-channel TLS the server has no identity to bind a proof to:
// it issues no challenge and says why. Password logins still work.
func TestKeyLoginNeedsAServerIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, peer, done := startPreauthPipe(t, 10, false)
		defer finishPreauthPipe(peer, done)
		if s.TLSFingerprint() != "" {
			t.Fatal("the pipe server has a certificate")
		}
		keys := newLinkTestKeys(t)
		send(t, peer, netproto.MsgAuthenticate, netproto.Authenticate{Username: keys.uniqueID, Anonymous: true})
		if resp := authReply(t, peer); resp.OK || resp.Reason != netproto.ReasonNoServerBinding {
			t.Fatalf("key login without TLS = %+v", resp)
		}
		send(t, peer, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: keys.public, Signature: make([]byte, 64)})
		if resp := authReply(t, peer); resp.OK || resp.Reason != "no pending challenge" {
			t.Fatalf("signature without a challenge = %+v", resp)
		}
	})
}

// The web app cannot see the certificate: its proof names the origin of the
// page, and the server checks it against the origin of that connection's
// own handshake.
func TestWebLoginProofIsBoundToTheHandshakeOrigin(t *testing.T) {
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) { cfg.AllowAnonymous = true }, nil)
	defer env.stop()
	keys := newLinkTestKeys(t)
	certificate := loginBindingOf(dialRetry(t, env.addr))
	for binding, want := range map[string]bool{
		netproto.LoginBindingOrigin(passkeyTestOrigin):          true,
		netproto.LoginBindingOrigin("https://other.example"):    false,
		netproto.LoginBindingOrigin(passkeyTestOrigin + ":444"): false,
		certificate: false, // the server's own certificate is not what a browser saw
	} {
		phone, serverSide := net.Pipe()
		go func() { _ = env.srv.ServeExternalConn(context.Background(), webPipe{serverSide}) }()
		challenge := loginChallengeOn(t, phone, netproto.Authenticate{Username: keys.uniqueID, Anonymous: true, Nickname: "phone"})
		signature, err := auth.SignLoginChallenge(keys.private, binding, challenge)
		if err != nil {
			t.Fatal(err)
		}
		send(t, phone, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: keys.public, Signature: signature, X25519PublicKey: keys.x25519})
		if resp := authReply(t, phone); resp.OK != want {
			t.Fatalf("web login bound to %q = %+v, want ok=%v", binding, resp, want)
		}
		_ = phone.Close()
	}
	// The same origin proof is worthless on the native port.
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	challenge := loginChallengeOn(t, conn, netproto.Authenticate{Username: keys.uniqueID, Anonymous: true, Nickname: "phone"})
	signature, _ := auth.SignLoginChallenge(keys.private, netproto.LoginBindingOrigin(passkeyTestOrigin), challenge)
	send(t, conn, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: keys.public, Signature: signature})
	if resp := authReply(t, conn); resp.OK {
		t.Fatalf("an origin-bound proof signed in on the native port: %+v", resp)
	}
}
