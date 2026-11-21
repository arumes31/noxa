package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// Login signatures are domain-separated: an updated client never signs raw
// server bytes, so a malicious server cannot obtain an identity signature on
// a device approval, link binding or any other "noxa-" transcript.
const testServerBinding = "tls-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

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
		if _, err := SignLoginChallenge(private, testServerBinding, c); !errors.Is(err, ErrUnsafeChallenge) {
			t.Fatalf("signed an unsafe challenge %q: %v", c, err)
		}
	}
	challenge, err := GenerateChallenge()
	if err != nil || len(challenge) != LoginChallengeSize {
		t.Fatal(err)
	}
	sig, err := SignLoginChallenge(private, testServerBinding, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyChallenge(public, challenge, sig) == nil {
		t.Fatal("an updated client must never produce a raw signature over the challenge")
	}
	if err := VerifyLoginChallenge(public, testServerBinding, challenge, sig); err != nil {
		t.Fatal("bound login must verify", err)
	}
	other := make([]byte, LoginChallengeSize)
	_, _ = rand.Read(other)
	if VerifyLoginChallenge(public, testServerBinding, other, sig) == nil {
		t.Fatal("a signature for another challenge must fail")
	}
}

// A login proof names the server it was made for: it verifies only against
// that server's identity, and the earlier unbound forms verify nowhere.
func TestLoginChallengeIsBoundToTheServer(t *testing.T) {
	public, private, err := GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignLoginChallenge(private, testServerBinding, challenge)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []string{"tls-sha256:" + strings.Repeat("b", 64), "origin:https://chat.example", testServerBinding + "\n"} {
		if VerifyLoginChallenge(public, binding, challenge, sig) == nil {
			t.Fatalf("a proof for one server verified for %q", binding)
		}
	}
	// No binding, no proof: neither side falls back to an unbound signature.
	for _, binding := range []string{"", "a\nb", strings.Repeat("a", 513)} {
		if _, err := SignLoginChallenge(private, binding, challenge); !errors.Is(err, ErrNoServerBinding) {
			t.Fatalf("signed for binding %q: %v", binding, err)
		}
		if err := VerifyLoginChallenge(public, binding, challenge, sig); !errors.Is(err, ErrNoServerBinding) {
			t.Fatalf("verified for binding %q: %v", binding, err)
		}
	}
	raw, _ := SignChallenge(private, challenge)
	unbound, _ := SignChallenge(private, append([]byte("noxa-auth-challenge-v1\n"), challenge...))
	for name, old := range map[string][]byte{"raw": raw, "unbound": unbound} {
		if VerifyLoginChallenge(public, testServerBinding, challenge, old) == nil {
			t.Fatalf("the %s form verified as a bound login", name)
		}
	}
}

// The vector the web app reproduces (mobile/unit/login-challenge.test.mjs).
func TestLoginChallengeMatchesTheSharedVector(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "mobile", "unit", "fixtures", "interop.json"))
	if err != nil {
		t.Skipf("mobile fixture not present: %v", err)
	}
	var fixture struct {
		Seed      []byte `json:"ed25519_seed"`
		Challenge []byte `json:"challenge"`
		Binding   string `json:"login_binding"`
		Signature []byte `json:"login_signature"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(fixture.Seed)
	message := LoginChallengeMessage(fixture.Binding, fixture.Challenge)
	if want := "noxa-auth-challenge-v2\n" + fixture.Binding + "\n" + string(fixture.Challenge); string(message) != want || fixture.Binding == "" {
		t.Fatalf("message = %q, want %q", message, want)
	}
	if !bytes.Equal(ed25519.Sign(private, message), fixture.Signature) {
		t.Fatal("the shared login vector does not match LoginChallengeMessage")
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: private.Public().(ed25519.PublicKey)}))
	if err := VerifyLoginChallenge(public, fixture.Binding, fixture.Challenge, fixture.Signature); err != nil {
		t.Fatal(err)
	}
}
