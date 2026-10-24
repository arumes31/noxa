package netproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/nacl/box"

	"noxa/internal/e2ee"
	"golang.org/x/crypto/nacl/secretbox"
)

var updateMobileFixture = flag.Bool("update-mobile-fixture", false, "rewrite mobile/unit/fixtures/interop.json")

// mobileInterop are values produced by the Go implementations that the
// mobile web app's JavaScript must reproduce or open.
type mobileInterop struct {
	Ed25519Seed     string `json:"ed25519_seed"`
	PublicKeyPEM    string `json:"public_key_pem"`
	UniqueID        string `json:"unique_id"`
	Fingerprint     string `json:"fingerprint"`
	Challenge       string `json:"challenge"`
	Signature       string `json:"signature"`
	LinkID          string `json:"link_id"`
	LinkSecret      string `json:"link_secret"`
	X25519Public    string `json:"x25519_public"`
	X25519Secret    string `json:"x25519_secret"`
	LinkProof       string `json:"link_proof"`
	LinkSAS         string `json:"link_sas"`
	CodeSAS         string `json:"code_sas"`
	Code            string `json:"code"`
	ScopeKey        string `json:"scope_key"`
	SealedScopeKey  string `json:"sealed_scope_key"`
	ScopeText       string `json:"scope_text"`
	ScopeBlob       string `json:"scope_blob"`
	PeerX25519      string `json:"peer_x25519_public"`
	PeerX25519Priv  string `json:"peer_x25519_secret"`
	DMText          string `json:"dm_text"`
	DMBlobFromPeer  string `json:"dm_blob_from_peer"`
	ApprovalPrinc   string `json:"approval_principal"`
	ApprovalDevice  string `json:"approval_device"`
	ApprovalMessage string `json:"approval_transcript"`
	ApprovalSig     string `json:"approval_signature"`
	KeyRecord       string `json:"key_record"`
}

func TestMobileInteropFixture(t *testing.T) {
	path := filepath.Join("..", "..", "mobile", "unit", "fixtures", "interop.json")
	if !*updateMobileFixture {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("mobile fixture not present: %v", err)
		}
		var fixture mobileInterop
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
		// The deterministic values must still match the Go implementation.
		seed, _ := base64.StdEncoding.DecodeString(fixture.Ed25519Seed)
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
		secret, _ := base64.StdEncoding.DecodeString(fixture.LinkSecret)
		transcript := LinkClaimTranscript(fixture.LinkID, pemText, fixture.X25519Public)
		if pemText != fixture.PublicKeyPEM || KeyFingerprint(pemText) != fixture.Fingerprint ||
			LinkSAS(secret, transcript) != fixture.LinkSAS ||
			base64.StdEncoding.EncodeToString(LinkProof(secret, transcript)) != fixture.LinkProof {
			t.Fatal("mobile interop fixture is stale; rerun with -update-mobile-fixture")
		}
		return
	}

	seed := sha256.Sum256([]byte("noxa mobile interop"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	sum := sha256.Sum256(pub)
	uniqueID := base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(sum[:])))
	challenge := []byte("challenge-bytes-0123456789abcdef")

	xPub, xPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerPub, peerPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	xPubB64 := b64(xPub[:])
	secret := sha256.Sum256([]byte("link secret"))
	transcript := LinkClaimTranscript("link-123", pemText, xPubB64)

	var scopeKey [32]byte
	copy(scopeKey[:], []byte("0123456789abcdef0123456789abcdef"))
	sealedKey, err := box.SealAnonymous(nil, scopeKey[:], xPub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [24]byte
	_, _ = rand.Read(nonce[:])
	scopeBlob := append(append([]byte{}, nonce[:]...), secretbox.Seal(nil, []byte("hello channel ✓"), &nonce, &scopeKey)...)
	_, _ = rand.Read(nonce[:])
	dmBlob := append(append([]byte{}, nonce[:]...), box.Seal(nil, []byte("hi from the desktop"), &nonce, xPub, peerPriv)...)

	fixture := mobileInterop{
		Ed25519Seed: b64(seed[:]), PublicKeyPEM: pemText, UniqueID: uniqueID, Fingerprint: KeyFingerprint(pemText),
		Challenge: b64(challenge), Signature: b64(ed25519.Sign(priv, challenge)),
		LinkID: "link-123", LinkSecret: b64(secret[:]), X25519Public: xPubB64, X25519Secret: b64(xPriv[:]),
		LinkProof: b64(LinkProof(secret[:], transcript)), LinkSAS: LinkSAS(secret[:], transcript),
		Code: "K7QM4XPA", CodeSAS: LinkSAS([]byte("K7QM4XPA"), transcript),
		ScopeKey: b64(scopeKey[:]), SealedScopeKey: b64(sealedKey), ScopeText: "hello channel ✓", ScopeBlob: b64(scopeBlob),
		PeerX25519: b64(peerPub[:]), PeerX25519Priv: b64(peerPriv[:]), DMText: "hi from the desktop", DMBlobFromPeer: b64(dmBlob),
		ApprovalPrinc: "user-uid", ApprovalDevice: "dev-desktop",
		ApprovalMessage: string(DeviceApprovalTranscript("user-uid", "dev-desktop", pemText, xPubB64)),
		ApprovalSig:     b64(ed25519.Sign(priv, DeviceApprovalTranscript("user-uid", "dev-desktop", pemText, xPubB64))),
	}
	record, err := e2ee.SignKeyRecord(uniqueID, *xPub, priv)
	if err != nil {
		t.Fatal(err)
	}
	fixture.KeyRecord = record
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
