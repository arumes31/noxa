package main

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// Review P2-2: a malicious server sending a device-approval or link-binding
// transcript as its login challenge gets no signature from the desktop.
func TestDesktopRefusesForgedLoginChallenges(t *testing.T) {
	id, err := newIdentity("signer")
	if err != nil {
		t.Fatal(err)
	}
	sign := loginSigner(id)
	for _, forged := range [][]byte{
		netproto.DeviceApprovalTranscript("uid", "desk", "pem", "x"),
		netproto.DeviceLinkBindingTranscript("uid", "desk", "pem", "x", "tls-sha256:"+strings.Repeat("ab", 32)),
		[]byte("noxa-" + strings.Repeat("z", 27)),
	} {
		if sig, _, err := sign(testLoginBinding, forged); err == nil || sig != nil {
			t.Fatalf("signed forged challenge %q", forged)
		}
	}
	challenge := make([]byte, auth.LoginChallengeSize)
	_, _ = rand.Read(challenge)
	challenge[0] = 'x'
	sig, pub, err := sign(testLoginBinding, challenge)
	if err != nil || auth.VerifyLoginChallenge(pub, testLoginBinding, challenge, sig) != nil || auth.VerifyChallenge(pub, challenge, sig) == nil {
		t.Fatalf("normal login must be domain-separated and verify: %v", err)
	}
	// The proof names one server; without a server identity nothing is signed.
	if auth.VerifyLoginChallenge(pub, "tls-sha256:"+strings.Repeat("cd", 32), challenge, sig) == nil {
		t.Fatal("a login proof verified for another server")
	}
	if sig, _, err := sign("", challenge); sig != nil || !errors.Is(err, auth.ErrNoServerBinding) {
		t.Fatalf("signed without a server binding: %v", err)
	}
}

var testLoginBinding = "tls-sha256:" + strings.Repeat("ab", 32)

// User decision (strict login): an older server that does not advertise the
// domain-separated scheme gets no signature; the client reports that the
// server needs an update.
func TestDesktopRefusesUnadvertisedLoginScheme(t *testing.T) {
	id, err := newIdentity("strict")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	signer := func(b string, c []byte) ([]byte, string, error) { called = true; return loginSigner(id)(b, c) }
	challenge := make([]byte, auth.LoginChallengeSize)
	_, _ = rand.Read(challenge)
	challenge[0] = 'x'
	for _, scheme := range []string{"", "noxa-auth-challenge-v1"} {
		if sig, _, reason := answerChallenge(netproto.AuthChallenge{Challenge: challenge, Scheme: scheme}, testLoginBinding, signer); reason != errServerLoginOutdated || sig != nil || called {
			t.Fatalf("old server (scheme %q): reason=%q signed=%v", scheme, reason, called)
		}
	}
	sig, pub, reason := answerChallenge(netproto.AuthChallenge{Challenge: challenge, Scheme: netproto.AuthChallengeSchemeV2}, testLoginBinding, signer)
	if reason != "" || auth.VerifyLoginChallenge(pub, testLoginBinding, challenge, sig) != nil {
		t.Fatalf("new server: %q", reason)
	}
	// A connection without TLS has no server identity: no signature, no retry loop.
	if sig, _, reason := answerChallenge(netproto.AuthChallenge{Challenge: challenge, Scheme: netproto.AuthChallengeSchemeV2}, "", signer); sig != nil || reason != auth.ErrNoServerBinding.Error() || !loginReasonTerminal(reason) {
		t.Fatalf("plaintext connection: reason=%q signed=%v", reason, sig != nil)
	}
}

// Fix2-review note: an unsafe challenge from a server that advertises the
// scheme is refused unsigned and is reconnect-terminal (no retry loop).
func TestUnsafeChallengeIsReconnectTerminal(t *testing.T) {
	id, err := newIdentity("unsafe")
	if err != nil {
		t.Fatal(err)
	}
	sig, _, reason := answerChallenge(netproto.AuthChallenge{Challenge: []byte("short"), Scheme: netproto.AuthChallengeSchemeV2}, testLoginBinding, loginSigner(id))
	if sig != nil || !loginReasonTerminal(reason) {
		t.Fatalf("unsafe challenge: reason=%q signed=%v", reason, sig != nil)
	}
	if !loginReasonTerminal(errServerLoginOutdated) || loginReasonTerminal("connection reset") {
		t.Fatal("terminal classification")
	}
}
