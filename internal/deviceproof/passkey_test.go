package deviceproof

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
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
	issuerPrivate     string
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
	cert := netproto.PasskeyCertificate{Algorithm: -7, Principal: "account", Issuer: "phone", CredentialID: "Y3JlZA", PublicKey: der, RPID: "example.test", Origin: "https://example.test", Chain: []netproto.DMDevice{{DeviceID: "master", FirstDevice: true, Ed25519Public: rootPEM, X25519Public: x}, issuer}}
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
	f := fixture{proof: netproto.PasskeyLoginProof{Certificate: cert, Nonce: make([]byte, 32)}, root: root, issuerPrivate: issuerPrivate, deviceEd: ed, deviceX: base64.StdEncoding.EncodeToString(targetX), key: key}
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

func TestPasskeyProofUsesCredentialAlgorithm(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p521, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		algorithm int64
		key       crypto.Signer
		hash      crypto.Hash
		pss       bool
	}{
		{"RSA-PSS256", -37, rsaKey, crypto.SHA256, true},
		{"RSA-PSS384", -38, rsaKey, crypto.SHA384, true},
		{"RSA-PSS512", -39, rsaKey, crypto.SHA512, true},
		{"RSA384", -258, rsaKey, crypto.SHA384, false},
		{"RSA512", -259, rsaKey, crypto.SHA512, false},
		{"ECDSA384", -35, p384, crypto.SHA384, false},
		{"ECDSA512", -36, p521, crypto.SHA512, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := makeFixture(t)
			f.proof.Certificate.Algorithm = tc.algorithm
			f.proof.Certificate.PublicKey, err = x509.MarshalPKIXPublicKey(tc.key.Public())
			if err != nil {
				t.Fatal(err)
			}
			f.proof.Certificate.Signature, err = auth.SignChallenge(f.issuerPrivate, f.proof.Certificate.Transcript())
			if err != nil {
				t.Fatal(err)
			}
			var assertion map[string]json.RawMessage
			if err = json.Unmarshal(f.proof.Assertion, &assertion); err != nil {
				t.Fatal(err)
			}
			var response map[string]string
			if err = json.Unmarshal(assertion["response"], &response); err != nil {
				t.Fatal(err)
			}
			data, _ := base64.RawURLEncoding.DecodeString(response["clientDataJSON"])
			ad, _ := base64.RawURLEncoding.DecodeString(response["authenticatorData"])
			clientHash := sha256.Sum256(data)
			h := tc.hash.New()
			h.Write(append(ad, clientHash[:]...))
			var opts crypto.SignerOpts = tc.hash
			if tc.pss {
				opts = &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: tc.hash}
			}
			sig, err := tc.key.Sign(rand.Reader, h.Sum(nil), opts)
			if err != nil {
				t.Fatal(err)
			}
			response["signature"] = base64.RawURLEncoding.EncodeToString(sig)
			assertion["response"], _ = json.Marshal(response)
			f.proof.Assertion, _ = json.Marshal(assertion)
			raw, _ := json.Marshal(f.proof)
			if err := Verify("account", f.root, f.deviceEd, f.deviceX, raw); err != nil {
				t.Fatal(err)
			}
			if tc.pss {
				f.proof.Certificate.Algorithm = -257
				f.proof.Certificate.Signature, _ = auth.SignChallenge(f.issuerPrivate, f.proof.Certificate.Transcript())
				raw, _ = json.Marshal(f.proof)
				if Verify("account", f.root, f.deviceEd, f.deviceX, raw) == nil {
					t.Fatal("algorithm substitution accepted")
				}
			}
		})
	}
}
