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
