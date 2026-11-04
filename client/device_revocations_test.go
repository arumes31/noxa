package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// A revocation made on this desktop is recorded locally, independently of a
// secure-messaging session, and filters this account's device list (cached or
// freshly verified) for every send path.
func TestOwnDeviceRevocationFiltersDeviceListAndReachesRatchet(t *testing.T) {
	cm, pub, _ := deviceCopyManager(t)
	phone := newTestDevice(t, "own-phone", "web")
	tablet := newTestDevice(t, "own-tablet", "web")
	_, epoch, _ := cm.dmDevices.get("me", time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub, First: true}, {DeviceID: phone.id, X25519: phone.xPublic}, {DeviceID: tablet.id, X25519: tablet.xPublic}}, epoch, time.Now())

	if err := cm.persistOwnDeviceRevocation(phone.id); err != nil {
		t.Fatal(err)
	}
	// persist forgets the cache; a replayed list still naming the phone is filtered.
	_, epoch, _ = cm.dmDevices.get("me", time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub, First: true}, {DeviceID: phone.id, X25519: phone.xPublic}, {DeviceID: tablet.id, X25519: tablet.xPublic}}, epoch, time.Now())
	devices, err := cm.dmDeviceList("me", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.DeviceID == phone.id {
			t.Fatal("a locally revoked device stayed a send target")
		}
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	// Own device and empty IDs are never recorded.
	if err := cm.persistOwnDeviceRevocation("self"); err != nil {
		t.Fatal(err)
	}
	if revoked, err := cm.ownRevokedDevices(); err != nil || len(revoked) != 1 || revoked[0] != phone.id {
		t.Fatalf("revoked = %v, %v", revoked, err)
	}

	// A session started later receives the recorded revocation.
	noxaPub, _, _ := ed25519.GenerateKey(rand.Reader)
	consumer, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return json.RawMessage("[]"), nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	consumer.d.DeviceKey = func(context.Context, string, string) (ed25519.PublicKey, error) { return noxaPub, nil }
	if err := cm.syncOwnRevocationsToRatchet(consumer); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.trust("me", phone.id); ratchetCode(err) != "revoked" {
		t.Fatalf("ratchet trust of a recorded revocation: %v", err)
	}
	if _, err := consumer.trust("me", tablet.id); err != nil {
		t.Fatalf("unrevoked device: %v", err)
	}
}
