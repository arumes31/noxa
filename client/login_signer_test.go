package main

import (
	"crypto/rand"
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
		if sig, _, err := sign(forged); err == nil || sig != nil {
			t.Fatalf("signed forged challenge %q", forged)
		}
	}
	challenge := make([]byte, auth.LoginChallengeSize)
	_, _ = rand.Read(challenge)
	challenge[0] = 'x'
	sig, pub, err := sign(challenge)
	if err != nil || auth.VerifyLoginChallenge(pub, challenge, sig) != nil || auth.VerifyChallenge(pub, challenge, sig) == nil {
		t.Fatalf("normal login must be domain-separated and verify: %v", err)
	}
}

// User decision (strict login): an older server that does not advertise the
// domain-separated scheme gets no signature; the client reports that the
// server needs an update.
func TestDesktopRefusesUnadvertisedLoginScheme(t *testing.T) {
	id, err := newIdentity("strict")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	signer := func(c []byte) ([]byte, string, error) { called = true; return loginSigner(id)(c) }
	challenge := make([]byte, auth.LoginChallengeSize)
	_, _ = rand.Read(challenge)
	challenge[0] = 'x'
	if sig, _, reason := answerChallenge(netproto.AuthChallenge{Challenge: challenge}, signer); reason != errServerLoginOutdated || sig != nil || called {
		t.Fatalf("old server: reason=%q signed=%v", reason, called)
	}
	sig, pub, reason := answerChallenge(netproto.AuthChallenge{Challenge: challenge, Scheme: netproto.AuthChallengeSchemeV1}, signer)
	if reason != "" || auth.VerifyLoginChallenge(pub, challenge, sig) != nil {
		t.Fatalf("new server: %q", reason)
	}
}
