package netproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// linkBindingVector is shared with mobile/unit/link-binding.test.mjs: the PWA
// verifies exactly this Go-produced statement (deterministic Ed25519).
func linkBindingVector() (approverPEM, devicePEM, x25519, binding string, signature []byte) {
	seed := func(base byte) ed25519.PrivateKey {
		s := make([]byte, 32)
		for i := range s {
			s[i] = base + byte(i)
		}
		return ed25519.NewKeyFromSeed(s)
	}
	pemOf := func(k ed25519.PrivateKey) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: k.Public().(ed25519.PublicKey)}))
	}
	approver, device := seed(1), seed(100)
	approverPEM, devicePEM = pemOf(approver), pemOf(device)
	x25519 = base64.StdEncoding.EncodeToString(make([]byte, 32))
	binding = "tls-sha256:" + strings.Repeat("ab", 32)
	signature = ed25519.Sign(approver, DeviceLinkBindingTranscript("alice", "desk", devicePEM, x25519, binding))
	return
}

func TestDeviceLinkBindingTranscriptVector(t *testing.T) {
	approverPEM, devicePEM, x25519, binding, signature := linkBindingVector()
	want := "noxa-device-link-binding-v1\nalice\ndesk\n" + devicePEM + "\n" + x25519 + "\n" + binding
	if got := string(DeviceLinkBindingTranscript("alice", "desk", devicePEM, x25519, binding)); got != want {
		t.Fatalf("transcript = %q", got)
	}
	const wantSig = "Kak/fnXht9tXtYMk67oi+hFrNyhG2QLcfsvCO26opePdVM1yBjor8LcXwXBXMDVp88OziP/ijwmCpi8dYzghAw=="
	if got := base64.StdEncoding.EncodeToString(signature); got != wantSig {
		t.Fatalf("vector signature changed: %s (approver %q)", got, approverPEM)
	}
	for b, ok := range map[string]bool{binding: true, "tls-sha256:" + strings.Repeat("AB", 32): false, "tls-sha256:ab": false, "sha256:" + strings.Repeat("ab", 32): false} {
		if ValidServerBinding(b) != ok {
			t.Fatalf("ValidServerBinding(%q) != %v", b, ok)
		}
	}
}
