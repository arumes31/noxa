package devicetrust

import (
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type testDevice struct {
	id, pub, priv string
	first         bool
}

func newTestDevice(t *testing.T, id string, first bool) testDevice {
	t.Helper()
	pub, priv, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return testDevice{id: id, pub: pub, priv: priv, first: first}
}

func (d testDevice) trusted(t *testing.T) Device {
	t.Helper()
	k, err := auth.LoadPublicKey(d.pub)
	if err != nil {
		t.Fatal(err)
	}
	return Device{ID: d.id, Signing: k, First: d.first}
}

const testX25519 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func signRevocation(t *testing.T, principal string, signer, target testDevice, signedAt int64) netproto.DeviceRevocation {
	t.Helper()
	tr := netproto.DeviceRevocationTranscript(principal, signer.id, target.id, target.pub, testX25519, signedAt, netproto.DeviceRevocationReasonUser)
	sig, err := auth.SignChallenge(signer.priv, tr)
	if err != nil {
		t.Fatal(err)
	}
	return netproto.DeviceRevocation{DeviceID: target.id, Ed25519Public: target.pub, X25519Public: testX25519, RevokedAt: signedAt,
		Reason: netproto.DeviceRevocationReasonUser, SignerDeviceID: signer.id, SignedAt: signedAt, Signature: sig}
}

func TestVerifyRevocationsAcceptsOnlyChainedSignatures(t *testing.T) {
	root := newTestDevice(t, "root", true)
	laptop := newTestDevice(t, "laptop", false)
	phone := newTestDevice(t, "phone", false)
	stranger := newTestDevice(t, "stranger", false)
	trusted := []Device{root.trusted(t), laptop.trusted(t)}

	good := signRevocation(t, "alice", laptop, phone, 1_800_000_000)
	byStranger := signRevocation(t, "alice", stranger, phone, 1_800_000_000)
	otherAccount := signRevocation(t, "mallory", laptop, phone, 1_800_000_000)
	rootByLaptop := signRevocation(t, "alice", laptop, root, 1_800_000_000)
	rootBySelf := signRevocation(t, "alice", root, root, 1_800_000_000)

	retargeted := good
	retargeted.DeviceID = "tablet" // replaying the signature for another device
	tamperedKey := signRevocation(t, "alice", laptop, phone, 1_800_000_000)
	tamperedKey.Ed25519Public = stranger.pub
	tamperedTime := good
	tamperedTime.SignedAt++
	unsigned := netproto.DeviceRevocation{DeviceID: "old", Ed25519Public: phone.pub, X25519Public: testX25519, RevokedAt: 1, Reason: "admin_reset"}

	got := VerifyRevocations("alice", trusted, []netproto.DeviceRevocation{
		byStranger, otherAccount, rootByLaptop, retargeted, tamperedKey, tamperedTime, unsigned, good, good, rootBySelf,
	})
	if len(got) != 2 || got[0].DeviceID != "phone" || got[0].SignerID != "laptop" || got[1].DeviceID != "root" || got[1].SignerID != "root" {
		t.Fatalf("verified = %+v, want phone (by laptop) and root (by itself) once each", got)
	}
}

func TestVerifyRevocationsBoundsTheList(t *testing.T) {
	root := newTestDevice(t, "root", true)
	var list []netproto.DeviceRevocation
	for i := 0; i < netproto.MaxListedRevocations+5; i++ {
		target := newTestDevice(t, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), false)
		list = append(list, signRevocation(t, "alice", root, target, 1_800_000_000))
	}
	if got := VerifyRevocations("alice", []Device{root.trusted(t)}, list); len(got) != netproto.MaxListedRevocations {
		t.Fatalf("verified %d, want the first %d", len(got), netproto.MaxListedRevocations)
	}
}
