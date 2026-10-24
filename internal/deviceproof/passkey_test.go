package deviceproof

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	"testing"
)

type fixture struct {
	proof             netproto.PasskeyLoginProof
	root              ed25519.PublicKey
	deviceEd, deviceX string
	key               *ecdsa.PrivateKey
}

func makeFixture(t *testing.T) fixture {
	t.Helper()
	rootPEM, rootPrivate, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := auth.LoadPublicKey(rootPEM)
	issuerPEM, issuerPrivate, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	x := base64.StdEncoding.EncodeToString(make([]byte, 32))
	issuer := netproto.DMDevice{DeviceID: "phone", Ed25519Public: issuerPEM, X25519Public: x, ApprovedBy: "master"}
	issuer.ApprovalSignature, err = auth.SignChallenge(rootPrivate, netproto.DeviceApprovalTranscript("account", "master", issuerPEM, x))
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := netproto.PasskeyCertificate{Principal: "account", Issuer: "phone", CredentialID: "Y3JlZA", PublicKey: der, RPID: "example.test", Origin: "https://example.test", Chain: []netproto.DMDevice{{DeviceID: "master", FirstDevice: true, Ed25519Public: rootPEM, X25519Public: x}, issuer}}
	cert.Signature, err = auth.SignChallenge(issuerPrivate, cert.Transcript())
	if err != nil {
		t.Fatal(err)
	}
	ed, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	targetX := make([]byte, 32)
	targetX[0] = 9
	f := fixture{proof: netproto.PasskeyLoginProof{Certificate: cert, Nonce: make([]byte, 32)}, root: root, deviceEd: ed, deviceX: base64.StdEncoding.EncodeToString(targetX), key: key}
	f.assertion(t, false, 5)
	return f
}
func (f *fixture) assertion(t *testing.T, cross bool, flags byte) {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	data, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": b64(netproto.PasskeyDeviceChallenge(f.proof.Nonce, f.deviceEd, f.deviceX)), "origin": "https://example.test", "crossOrigin": cross})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte("example.test"))
	ad := append(append([]byte{}, rp[:]...), flags, 0, 0, 0, 1)
	hash := sha256.Sum256(data)
	signed := append(append([]byte{}, ad...), hash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	f.proof.Assertion, err = json.Marshal(map[string]any{"type": "public-key", "rawId": "Y3JlZA", "response": map[string]string{"clientDataJSON": b64(data), "authenticatorData": b64(ad), "signature": b64(sig)}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPasskeyProofRequiresPinnedChainAndBoundVerifiedAssertion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		valid  bool
	}{
		{"valid", func(*fixture) {}, true},
		{"other account", func(f *fixture) { f.proof.Certificate.Principal = "other" }, false},
		{"forged delegation", func(f *fixture) { f.proof.Certificate.Signature[0] ^= 1 }, false},
		{"forged device approval", func(f *fixture) { f.proof.Certificate.Chain[1].ApprovalSignature[0] ^= 1 }, false},
		{"cyclic issuer", func(f *fixture) { f.proof.Certificate.Chain[1].ApprovedBy = "phone" }, false},
		{"wrong root", func(f *fixture) { key, _, _ := ed25519.GenerateKey(rand.Reader); f.root = key }, false},
		{"duplicate device", func(f *fixture) {
			f.proof.Certificate.Chain = append(f.proof.Certificate.Chain, f.proof.Certificate.Chain[0])
		}, false},
		{"new encryption key", func(f *fixture) {
			f.deviceX = base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
		}, false},
		{"new signing key", func(f *fixture) { f.deviceEd, _, _ = auth.GenerateIdentityKeyPair() }, false},
		{"different nonce", func(f *fixture) { f.proof.Nonce[0] ^= 1 }, false},
		{"different credential", func(f *fixture) { f.proof.Certificate.CredentialID = "b3RoZXI" }, false},
		{"cross origin", func(f *fixture) { f.assertion(t, true, 5) }, false},
		{"no user verification", func(f *fixture) { f.assertion(t, false, 1) }, false},
		{"no user presence", func(f *fixture) { f.assertion(t, false, 4) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := makeFixture(t)
			tc.change(&f)
			raw, err := json.Marshal(f.proof)
			if err != nil {
				t.Fatal(err)
			}
			err = Verify("account", f.root, f.deviceEd, f.deviceX, raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
