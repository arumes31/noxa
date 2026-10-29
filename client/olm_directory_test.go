package main

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeOlmDirectoryConsumer(t *testing.T) {
	var f struct {
		Server         string
		Principal      string
		DeviceID       string `json:"device_id"`
		RootSigning    []byte `json:"root_signing"`
		RootEncryption []byte `json:"root_encryption"`
		Devices        []netproto.DMDevice
		Bundle         olmdir.Bundle
		KeyID          string `json:"key_id"`
		Now            int64
	}
	data, e := os.ReadFile("../internal/e2ee/olmdir/testdata/directory-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	var root [32]byte
	copy(root[:], f.RootEncryption)
	key, e := verifyOlmClaim(f.Bundle, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, f.RootSigning, root, f.Devices, time.Unix(f.Now, 0))
	if e != nil || key.ID != f.KeyID {
		t.Fatal("native consumer", e)
	}
	f.Devices[0].RevokedAt = f.Now
	if _, e = verifyOlmClaim(f.Bundle, f.Server, f.Principal, f.DeviceID, f.Bundle.Identity, f.KeyID, f.RootSigning, root, f.Devices, time.Unix(f.Now, 0)); e == nil {
		t.Fatal("revoked root trusted")
	}
}

func TestNativeReservedClaimAndEvidence(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	curve := func() string {
		k, _ := ecdh.X25519().GenerateKey(rand.Reader)
		return base64.RawStdEncoding.EncodeToString(k.PublicKey().Bytes())
	}
	signing := base64.RawStdEncoding.EncodeToString(pub)
	now := time.Now()
	server := "tls-sha256:" + strings.Repeat("c", 64)
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: server, Principal: "bob", DeviceID: "bob-1", AccountEpoch: 1, Generation: 1,
		IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 3600, Identity: curve(), Signing: signing, OneTimeKeys: []olmdir.Key{{ID: "AAAAAAAAAAE", Key: curve()}}}
	signed, e := b.Sign(priv)
	if e != nil {
		t.Fatal(e)
	}
	x := olmTrustExpectation{SigningKey: pub, Server: server, Principal: "bob", Device: "bob-1", AccountEpoch: 1, Identity: signed.Identity}
	until := now.Unix() + 600
	if k, e := verifyReservedClaim(&signed, x, "AAAAAAAAAAE", until, now); e != nil || k.Key != signed.OneTimeKeys[0].Key {
		t.Fatal("valid claim refused", e)
	}
	if e := verifyOlmBundleEvidence(signed, x); e != nil {
		t.Fatal("valid evidence refused", e)
	}
	for name, mutate := range map[string]func(*olmTrustExpectation, *string, *int64){
		"unpinned signer":      func(x *olmTrustExpectation, _ *string, _ *int64) { x.SigningKey = other },
		"changed epoch":        func(x *olmTrustExpectation, _ *string, _ *int64) { x.AccountEpoch = 2 },
		"changed identity":     func(x *olmTrustExpectation, _ *string, _ *int64) { x.Identity = curve() },
		"no expected identity": func(x *olmTrustExpectation, _ *string, _ *int64) { x.Identity = "" },
		"other device":         func(x *olmTrustExpectation, _ *string, _ *int64) { x.Device = "bob-2" },
		"other server":         func(x *olmTrustExpectation, _ *string, _ *int64) { x.Server = "tls-sha256:" + strings.Repeat("d", 64) },
		"unlisted key":         func(_ *olmTrustExpectation, k *string, _ *int64) { *k = "AAAAAAAAAAI" },
		"claim already over":   func(_ *olmTrustExpectation, _ *string, u *int64) { *u = now.Unix() - 1 },
		"claim beyond bundle":  func(_ *olmTrustExpectation, _ *string, u *int64) { *u = signed.ExpiresAt + 7200 },
	} {
		cx, key, u := x, "AAAAAAAAAAE", until
		mutate(&cx, &key, &u)
		if _, e := verifyReservedClaim(&signed, cx, key, u, now); e == nil {
			t.Fatalf("%s: claim accepted", name)
		}
	}
	if _, e := verifyReservedClaim(nil, x, "AAAAAAAAAAE", until, now); e == nil {
		t.Fatal("missing bundle accepted")
	}
	tampered := signed
	tampered.OneTimeKeys = []olmdir.Key{{ID: "AAAAAAAAAAE", Key: curve()}}
	if _, e := verifyReservedClaim(&tampered, x, "AAAAAAAAAAE", until, now); e == nil {
		t.Fatal("server-substituted one-time key accepted")
	}
	if e := verifyOlmBundleEvidence(tampered, x); e == nil {
		t.Fatal("tampered evidence accepted")
	}
	// Retained evidence has no publication TTL: an expired bundle still
	// authenticates an admitted delayed initial packet.
	if e := verifyOlmBundleEvidence(signed, x); e != nil {
		t.Fatal(e)
	}
}
