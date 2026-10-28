package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"noxa/internal/netproto"
)

// Login signatures are domain-separated: an updated client never signs raw
// server bytes, so a malicious server cannot obtain an identity signature on
// a device approval, link binding or any other "noxa-" transcript.
func TestLoginChallengeIsDomainSeparated(t *testing.T) {
	public, private, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	forged := [][]byte{
		netproto.DeviceApprovalTranscript("alice", "desk", "-----BEGIN PUBLIC KEY-----\nX\n-----END PUBLIC KEY-----\n", "x"),
		netproto.DeviceLinkBindingTranscript("alice", "desk", "pem", "x", "tls-sha256:"+string(bytes.Repeat([]byte("a"), 64))),
		[]byte("noxa-device-approval-v1\nalice\n12345"), // 32 bytes, "noxa-" prefix
		make([]byte, 31), make([]byte, 33),
	}
	for _, c := range forged {
		if _, err := SignLoginChallenge(private, c); !errors.Is(err, ErrUnsafeChallenge) {
			t.Fatalf("signed an unsafe challenge %q: %v", c, err)
		}
	}
	challenge, err := GenerateChallenge()
	if err != nil || len(challenge) != LoginChallengeSize {
		t.Fatal(err)
	}
	sig, err := SignLoginChallenge(private, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyChallenge(public, challenge, sig) == nil {
		t.Fatal("an updated client must never produce a raw signature over the challenge")
	}
	if err := VerifyLoginChallenge(public, challenge, sig); err != nil {
		t.Fatal("domain-separated login must verify", err)
	}
	legacy, _ := SignChallenge(private, challenge)
	if err := VerifyLoginChallenge(public, challenge, legacy); err != nil {
		t.Fatal("older clients' raw login must still verify", err)
	}
	other := make([]byte, LoginChallengeSize)
	_, _ = rand.Read(other)
	if VerifyLoginChallenge(public, other, sig) == nil || VerifyLoginChallenge(public, other, legacy) == nil {
		t.Fatal("a signature for another challenge must fail")
	}
}
