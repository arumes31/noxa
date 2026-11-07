package store

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestSignedDeviceRevocationsAreListedWithTheirStatement(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("revocation-principal-%d", time.Now().UnixNano())

	root := newTestDevice(principal, 0)
	root.Kind = DeviceKindDesktop
	d0, err := s.AddDevice(ctx, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	approved := func(n int) Device {
		nd := newTestDevice(principal, n)
		nd.ApprovedBy, nd.ApprovalSignature = d0.DeviceID, bytes.Repeat([]byte{byte(n)}, 64)
		d, err := s.AddDevice(ctx, nd, 5)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	phone, tablet := approved(1), approved(2)
	unconfirmed, err := s.AddDevice(ctx, newTestDevice(principal, 3), 5) // never approved
	if err != nil {
		t.Fatal(err)
	}

	sig := SignedRevocation{SignerDeviceID: d0.DeviceID, SignedAt: 1_800_000_000, Signature: bytes.Repeat([]byte{7}, 64)}
	if _, err := s.RevokeDeviceSigned(ctx, principal, phone.DeviceID, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(ctx, principal, tablet.DeviceID, DeviceRevokedByAdminReset); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(ctx, principal, unconfirmed.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDeviceSigned(ctx, principal, phone.DeviceID, SignedRevocation{SignerDeviceID: "x", SignedAt: 1, Signature: []byte{1}}); err == nil {
		t.Fatal("a malformed signature was accepted")
	}

	list, err := s.ListDeviceRevocations(ctx, principal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d revocations, want the two approved devices (not the unconfirmed one): %+v", len(list), list)
	}
	byID := map[string]DeviceRevocation{}
	for _, r := range list {
		byID[r.DeviceID] = r
	}
	signed := byID[phone.DeviceID]
	if signed.SignerDeviceID != d0.DeviceID || signed.SignedAt != sig.SignedAt || !bytes.Equal(signed.Signature, sig.Signature) ||
		signed.Ed25519Public != phone.Ed25519Public || signed.Reason != DeviceRevokedByUser || signed.RevokedAt.IsZero() {
		t.Fatalf("signed revocation = %+v", signed)
	}
	server := byID[tablet.DeviceID]
	if server.SignerDeviceID != "" || len(server.Signature) != 0 || server.Reason != DeviceRevokedByAdminReset {
		t.Fatalf("server-only revocation = %+v", server)
	}
	if limited, err := s.ListDeviceRevocations(ctx, principal, 1); err != nil || len(limited) != 1 {
		t.Fatalf("limit 1 listed %d, %v", len(limited), err)
	}
	if none, err := s.ListDeviceRevocations(ctx, "nobody-"+principal, 10); err != nil || len(none) != 0 {
		t.Fatalf("another account listed %d, %v", len(none), err)
	}
}

func TestSignedRevocationKeepsTheFirstStatement(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("revocation-repeat-%d", time.Now().UnixNano())
	root := newTestDevice(principal, 0)
	root.Kind = DeviceKindDesktop
	d0, err := s.AddDevice(ctx, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	nd := newTestDevice(principal, 1)
	nd.ApprovedBy, nd.ApprovalSignature = d0.DeviceID, bytes.Repeat([]byte{1}, 64)
	phone, err := s.AddDevice(ctx, nd, 5)
	if err != nil {
		t.Fatal(err)
	}
	first := SignedRevocation{SignerDeviceID: d0.DeviceID, SignedAt: 100, Signature: bytes.Repeat([]byte{1}, 64)}
	if _, err := s.RevokeDeviceSigned(ctx, principal, phone.DeviceID, first); err != nil {
		t.Fatal(err)
	}
	// A device without a passkey cannot be revoked twice.
	if _, err := s.RevokeDeviceSigned(ctx, principal, phone.DeviceID, SignedRevocation{SignerDeviceID: d0.DeviceID, SignedAt: 200, Signature: bytes.Repeat([]byte{2}, 64)}); err == nil {
		t.Fatal("revoking a revoked device again succeeded")
	}
	list, err := s.ListDeviceRevocations(ctx, principal, 10)
	if err != nil || len(list) != 1 || list[0].SignedAt != 100 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}
