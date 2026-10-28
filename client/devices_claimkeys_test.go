package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noxa/internal/auth"
)

// CodeRabbit fix2 (major): link decide must accept what real claiming devices
// send. The PWA's publicKeyPEM output is pinned against Go's encoding by the
// shared interop fixture (mobile/unit/crypto.test.mjs asserts the JS side);
// the desktop's own identity PEM is the other producer.
func TestClaimedDeviceKeysAcceptRealProducers(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "mobile", "unit", "fixtures", "interop.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		PEM    string `json:"public_key_pem"`
		X25519 string `json:"x25519_public"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if !validClaimedDeviceKeys(fx.PEM, fx.X25519) {
		t.Fatal("the PWA's real claim keys are refused")
	}
	pub, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if !validClaimedDeviceKeys(pub, fx.X25519) {
		t.Fatal("a Go-encoded identity PEM is refused")
	}
	// Benign formatting is canonicalized, then validated.
	for _, benign := range []string{strings.ReplaceAll(fx.PEM, "\n", "\r\n"), strings.TrimSuffix(fx.PEM, "\n")} {
		if !validClaimedDeviceKeys(benign, fx.X25519) {
			t.Fatalf("benign formatting refused: %q", benign)
		}
	}
	// Garbage or extra lines inside a field never pass.
	lines := strings.Split(fx.PEM, "\n")
	for _, bad := range [][2]string{
		{fx.PEM + "extra\n", fx.X25519},
		{lines[0] + "\nx: y\n" + strings.Join(lines[1:], "\n"), fx.X25519},
		{strings.Replace(fx.PEM, "\n", "\n\n", 1), fx.X25519},
		{" " + fx.PEM, fx.X25519},
		{fx.PEM, fx.X25519 + "\n"},
		{fx.PEM, " " + fx.X25519},
		{fx.PEM, strings.TrimRight(fx.X25519, "=")},
		{fx.PEM, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
	} {
		if validClaimedDeviceKeys(bad[0], bad[1]) {
			t.Fatalf("accepted %q / %q", bad[0], bad[1])
		}
	}
}
