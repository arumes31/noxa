package main

import (
	"sync"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type revocationEventSink struct {
	mu     sync.Mutex
	events []DeviceRevokedEvent
}

func (s *revocationEventSink) Emit(name string, payload any) {
	if name != "device_revoked" {
		return
	}
	s.mu.Lock()
	s.events = append(s.events, payload.(DeviceRevokedEvent))
	s.mu.Unlock()
}

func TestWithoutPinnedRevocationsDropsByIDAndKey(t *testing.T) {
	phonePub, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	phoneKey, err := auth.LoadPublicKey(phonePub)
	if err != nil {
		t.Fatal(err)
	}
	laptopPub, _, _ := auth.GenerateIdentityKeyPair()
	laptopKey, _ := auth.LoadPublicKey(laptopPub)
	devices := []dmTrustedDevice{
		{DeviceID: "root", First: true},
		{DeviceID: "renamed-phone", Signing: phoneKey}, // same key under a new ID
		{DeviceID: "tablet"},
		{DeviceID: "laptop", Signing: laptopKey},
	}
	pins := []signedRevocationPin{
		{DeviceID: "phone", Ed25519Public: phonePub},
		{DeviceID: "tablet", Ed25519Public: "not a key"},
	}
	got := withoutPinnedRevocations(devices, pins)
	if len(got) != 2 || got[0].DeviceID != "root" || got[1].DeviceID != "laptop" {
		t.Fatalf("kept %+v, want root and laptop", got)
	}
	if len(withoutPinnedRevocations(devices, nil)) != 4 {
		t.Fatal("no pins dropped devices")
	}
}

func TestRevocationNoticesSkipTheFirstListsServerRevocations(t *testing.T) {
	sink := &revocationEventSink{}
	m := &connManager{sink: sink}
	sig := make([]byte, 64)
	old := netproto.DeviceRevocation{DeviceID: "old", RevokedAt: 1, Reason: "admin_reset"}
	signedOld := netproto.DeviceRevocation{DeviceID: "pinned-before", RevokedAt: 2, Reason: "user", SignerDeviceID: "root", SignedAt: 2, Signature: sig}

	// First list this session: old server-only revocations are the baseline;
	// a signed one already pinned in an earlier session is not new.
	m.noticeDeviceRevocations("bob", []netproto.DeviceRevocation{old, signedOld}, nil)
	if len(sink.events) != 0 {
		t.Fatalf("first list reported %+v", sink.events)
	}

	fresh := netproto.DeviceRevocation{DeviceID: "tablet", RevokedAt: 3, Reason: "idle"}
	signedNew := netproto.DeviceRevocation{DeviceID: "phone", RevokedAt: 4, Reason: "user", SignerDeviceID: "laptop", SignedAt: 4, Signature: sig}
	forged := netproto.DeviceRevocation{DeviceID: "forged", RevokedAt: 5, Reason: "user", SignerDeviceID: "laptop", SignedAt: 5, Signature: sig}
	m.noticeDeviceRevocations("bob", []netproto.DeviceRevocation{old, fresh, signedNew, forged},
		[]signedRevocationPin{{DeviceID: "phone", SignerID: "laptop"}})
	if len(sink.events) != 2 {
		t.Fatalf("events = %+v, want the new server revocation and the newly pinned signed one", sink.events)
	}
	byID := map[string]DeviceRevokedEvent{}
	for _, e := range sink.events {
		byID[e.DeviceID] = e
	}
	if e := byID["tablet"]; e.Signed || e.UniqueID != "bob" {
		t.Fatalf("server revocation event = %+v", e)
	}
	if e := byID["phone"]; !e.Signed || e.SignerID != "laptop" {
		t.Fatalf("signed revocation event = %+v", e)
	}

	// Nothing is reported twice; an unverified signed entry never is.
	m.noticeDeviceRevocations("bob", []netproto.DeviceRevocation{old, fresh, signedNew, forged}, nil)
	if len(sink.events) != 2 {
		t.Fatalf("repeated events: %+v", sink.events)
	}

	// The baseline is per account.
	m.noticeDeviceRevocations("carol", []netproto.DeviceRevocation{old}, nil)
	if len(sink.events) != 2 {
		t.Fatalf("carol's first list reported %+v", sink.events)
	}
}
