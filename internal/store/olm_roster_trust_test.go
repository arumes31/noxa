package store

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"slices"
	"testing"
	"time"

	"noxa/internal/devicetrust"
	"noxa/internal/netproto"
)

type chainDevice struct {
	d    Device
	priv ed25519.PrivateKey
	x    [32]byte
	src  OlmSource
}

// publishedChainDevice registers a device with real keys, approved by
// approver (nil: the first device), and publishes its Olm bundle.
func publishedChainDevice(t *testing.T, s *Store, principal, kind string, approver *chainDevice) chainDevice {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	xk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	x := base64.StdEncoding.EncodeToString(xk.PublicKey().Bytes())
	nd := NewDevice{Principal: principal, Kind: kind, Ed25519Public: pubText, X25519Public: x}
	if approver != nil {
		nd.ApprovedBy = approver.d.DeviceID
		nd.ApprovalSignature = ed25519.Sign(approver.priv, netproto.DeviceApprovalTranscript(principal, approver.d.DeviceID, pubText, x))
	}
	d, _, b := olmFixtureFor(t, s, nd, priv)
	if err := s.PublishOlmBundle(t.Context(), principal, d.DeviceID, b.Server, b); err != nil {
		t.Fatal(err)
	}
	out := chainDevice{d: d, priv: priv, src: OlmSource{Server: b.Server, Principal: principal, Device: d.DeviceID, Signing: d.Ed25519Public, AccountEpoch: 1}}
	copy(out.x[:], xk.PublicKey().Bytes())
	return out
}

func snapshotDevices(t *testing.T, snap OlmRosterSnapshot) []string {
	t.Helper()
	var ids []string
	for _, target := range snap.Targets {
		ids = append(ids, target.Device)
	}
	return ids
}

// F1: a device whose approver was revoked is no longer trusted by clients
// (devicetrust.Verify), so neither the device directory (DMTrusted) nor the
// Olm roster may require it. The same listing is checked by both rules.
func TestOlmRosterDropsDeviceOfRevokedApprover(t *testing.T) {
	s := pollTestStore(t)
	alice := publishedChainDevice(t, s, "alice", DeviceKindDesktop, nil)
	root := publishedChainDevice(t, s, "bob", DeviceKindDesktop, nil)
	laptop := publishedChainDevice(t, s, "bob", DeviceKindDesktop, &root)
	web := publishedChainDevice(t, s, "bob", DeviceKindWeb, &laptop)

	agree := func(want []string) {
		t.Helper()
		devices, err := s.ListDevices(t.Context(), "bob", false)
		if err != nil {
			t.Fatal(err)
		}
		var list []netproto.DMDevice
		var server []string
		for _, d := range devices {
			list = append(list, netproto.DMDevice{DeviceID: d.DeviceID, Kind: d.Kind, Ed25519Public: d.Ed25519Public, X25519Public: d.X25519Public,
				FirstDevice: d.FirstDevice, ApprovedBy: d.ApprovedBy, ApprovalSignature: d.ApprovalSignature})
		}
		for _, d := range DMTrusted("bob", devices) {
			server = append(server, d.DeviceID)
		}
		verified, err := devicetrust.Verify("bob", root.priv.Public().(ed25519.PublicKey), root.x, list)
		if err != nil {
			t.Fatal(err)
		}
		var client []string
		for _, d := range verified {
			client = append(client, d.ID)
		}
		slices.Sort(server)
		slices.Sort(client)
		slices.Sort(want)
		if !slices.Equal(server, want) || !slices.Equal(client, want) {
			t.Fatalf("server trusts %v, client trusts %v, want %v", server, client, want)
		}
	}
	agree([]string{root.d.DeviceID, laptop.d.DeviceID, web.d.DeviceID})
	snap, err := s.OlmSnapshot(t.Context(), alice.src, "direct", "bob", "")
	if err != nil || !slices.Contains(snapshotDevices(t, snap), web.d.DeviceID) {
		t.Fatalf("before revocation: %v %v", snapshotDevices(t, snap), err)
	}

	if _, err := s.RevokeDevice(t.Context(), "bob", laptop.d.DeviceID, "idle"); err != nil {
		t.Fatal(err)
	}
	agree([]string{root.d.DeviceID})
	snap, err = s.OlmSnapshot(t.Context(), alice.src, "direct", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotDevices(t, snap); len(got) != 2 || slices.Contains(got, web.d.DeviceID) {
		t.Fatalf("orphaned device still required: %v", got)
	}
	// A revoked approver cannot approve inside the endorsement transaction.
	extra, _, _ := olmFixture(t, s, "bob")
	if _, err := s.EndorseDevice(t.Context(), "bob", extra.DeviceID, laptop.d.DeviceID, make([]byte, 64)); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("endorsement by a revoked approver: %v", err)
	}
}

// F2: an approved, idle web device whose bundle expired cannot be claimed
// (ClaimOlmKey uses unexpired publications only); it must not block a first
// contact with its owner's other devices.
func TestOlmRosterSkipsIdleExpiredPublication(t *testing.T) {
	s := pollTestStore(t)
	alice := publishedChainDevice(t, s, "alice", DeviceKindDesktop, nil)
	root := publishedChainDevice(t, s, "bob", DeviceKindDesktop, nil)
	web := publishedChainDevice(t, s, "bob", DeviceKindWeb, &root)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE olm_directory_publications SET expires_at=now()-interval '1 hour' WHERE device_id=$1`, web.d.DeviceID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Unix() + 300
	// Recently seen: still required (its owner is expected to republish).
	snap, err := s.OlmSnapshot(t.Context(), alice.src, "direct", "bob", "")
	if err != nil || !slices.Contains(snapshotDevices(t, snap), web.d.DeviceID) {
		t.Fatalf("recently seen device: %v %v", snapshotDevices(t, snap), err)
	}
	if _, err = s.ClaimOlmKey(t.Context(), "alice", alice.d.DeviceID, "bob", web.d.DeviceID, "c-web", until, alice.src.Server); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("claim from an expired publication: %v", err)
	}

	idleOlmDevice(t, s, web.d.DeviceID)
	snap, err = s.OlmSnapshot(t.Context(), alice.src, "direct", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotDevices(t, snap); len(got) != 2 || slices.Contains(got, web.d.DeviceID) {
		t.Fatalf("idle expired device still required: %v", got)
	}
	// Desktop-style first contact: a claim for every non-local target.
	for _, target := range snap.Targets {
		if target.Local {
			continue
		}
		if _, err = s.ClaimOlmKey(t.Context(), "alice", alice.d.DeviceID, "bob", target.Device, "c-"+target.Device, until, alice.src.Server); err != nil {
			t.Fatalf("first-contact claim for %s: %v", target.Device, err)
		}
	}
}

// F14: closed/received one-time keys drop their bundle copy, and rows of
// devices revoked a day ago are deleted, by the bounded directory sweep.
func TestExpireOlmDirectoryTrimsKeyRows(t *testing.T) {
	s := pollTestStore(t)
	kept := publishedChainDevice(t, s, "alice", DeviceKindDesktop, nil)
	gone := publishedChainDevice(t, s, "alice", DeviceKindDesktop, &kept)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE olm_directory_keys SET lifecycle=CASE WHEN key_id=(SELECT min(key_id) FROM olm_directory_keys WHERE device_id=$1) THEN 'closed' ELSE 'received' END
 WHERE device_id=$1 AND key_id IN (SELECT key_id FROM olm_directory_keys WHERE device_id=$1 ORDER BY key_id LIMIT 2)`, kept.d.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(t.Context(), "alice", gone.d.DeviceID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE user_devices SET revoked_at=now()-interval '2 days' WHERE device_id=$1`, gone.d.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireOlmDirectory(t.Context()); err != nil {
		t.Fatal(err)
	}
	var cleared, full, goneRows int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE octet_length(bundle)=0 AND lifecycle IN ('closed','received')),count(*) FILTER(WHERE octet_length(bundle)>0 AND lifecycle='available'),
 (SELECT count(*) FROM olm_directory_keys WHERE device_id=$2) FROM olm_directory_keys WHERE device_id=$1`, kept.d.DeviceID, gone.d.DeviceID).Scan(&cleared, &full, &goneRows); err != nil {
		t.Fatal(err)
	}
	if cleared != 2 || full != 8 || goneRows != 0 {
		t.Fatalf("cleared %d, untouched available %d, revoked device rows %d", cleared, full, goneRows)
	}
}

// F4/F5 server side: the claimable count drops with a claim, and a replayed
// claim request returns the same key only while that key is still reserved.
func TestOlmClaimReplayOnlyWhileReserved(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	if n, err := s.AvailableOlmKeys(t.Context(), b.Device, b.Server); err != nil || n != 9 {
		t.Fatalf("available after one claim of ten: %d %v", n, err)
	}
	first, err := s.ClaimOlmKey(t.Context(), a.Principal, a.Device, b.Principal, b.Device, "claim", m.Deadline, a.Server)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.ClaimOlmKey(t.Context(), a.Principal, a.Device, b.Principal, b.Device, "claim", m.Deadline, a.Server); err != nil || again.KeyID != first.KeyID {
		t.Fatalf("replay of a reserved claim: %v %v", again.KeyID, err)
	}
	if _, err = s.BeginOlmAttempt(t.Context(), a, m); err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if err = s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID); err != nil || r.State != "accepted" {
		t.Fatalf("admission: %+v %v", r, err)
	}
	if _, err = s.ClaimOlmKey(t.Context(), a.Principal, a.Device, b.Principal, b.Device, "claim", m.Deadline, a.Server); !errors.Is(err, ErrOlmExhausted) {
		t.Fatalf("replay of an admitted claim: %v", err)
	}
}
