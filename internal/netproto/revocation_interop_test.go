package netproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// revocationInterop is a signed device revocation (38) produced by Go that
// the mobile web app must verify byte for byte (mobile/unit/fixtures/
// revocation.json). Every value is deterministic, so the test regenerates the
// fixture and compares it with the committed file.
type revocationInterop struct {
	Principal  string `json:"principal"`
	SignerID   string `json:"signer_device_id"`
	SignerPEM  string `json:"signer_public_key_pem"`
	DeviceID   string `json:"device_id"`
	DevicePEM  string `json:"device_public_key_pem"`
	X25519     string `json:"x25519_public_key"`
	SignedAt   int64  `json:"signed_at"`
	Transcript string `json:"transcript"`
	Signature  string `json:"signature"`
}

func buildRevocationInterop() revocationInterop {
	key := func(label string) (ed25519.PrivateKey, string) {
		seed := sha256.Sum256([]byte(label))
		priv := ed25519.NewKeyFromSeed(seed[:])
		return priv, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: priv.Public().(ed25519.PublicKey)}))
	}
	signer, signerPEM := key("noxa revocation signer")
	_, devicePEM := key("noxa revoked device")
	x := sha256.Sum256([]byte("noxa revoked device x25519"))
	r := revocationInterop{Principal: "user-uid", SignerID: "dev-desktop", SignerPEM: signerPEM, DeviceID: "dev-phone",
		DevicePEM: devicePEM, X25519: base64.StdEncoding.EncodeToString(x[:]), SignedAt: 1_800_000_000}
	transcript := DeviceRevocationTranscript(r.Principal, r.SignerID, r.DeviceID, r.DevicePEM, r.X25519, r.SignedAt, DeviceRevocationReasonUser)
	r.Transcript = string(transcript)
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signer, transcript))
	return r
}

func TestMobileRevocationFixture(t *testing.T) {
	want, err := json.MarshalIndent(buildRevocationInterop(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("..", "..", "mobile", "unit", "fixtures", "revocation.json")
	if *updateMobileFixture {
		if err := os.WriteFile(path, want, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (rerun with -update-mobile-fixture)", path, err)
	}
	// Compare values, not bytes: a checkout may rewrite line endings.
	var got revocationInterop
	if err := json.Unmarshal(raw, &got); err != nil || got != buildRevocationInterop() {
		t.Fatal("mobile revocation fixture is stale; rerun with -update-mobile-fixture")
	}
}
