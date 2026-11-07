package server

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/devicetrust"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestVerifyRevocationSignature(t *testing.T) {
	signerPub, signerPriv, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	targetPub, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	signer := store.Device{DeviceID: "laptop", Ed25519Public: signerPub, ApprovedBy: "root", ApprovalSignature: bytes.Repeat([]byte{1}, 64)}
	target := store.Device{DeviceID: "phone", Ed25519Public: targetPub, X25519Public: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	sign := func(principal string, at int64, tgt store.Device) []byte {
		sig, err := auth.SignChallenge(signerPriv, netproto.DeviceRevocationTranscript(principal, signer.DeviceID, tgt.DeviceID, tgt.Ed25519Public, tgt.X25519Public, at, netproto.DeviceRevocationReasonUser))
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
	good := sign("alice", now.Unix(), target)
	if err := verifyRevocationSignature("alice", signer, target, now.Unix(), good, now); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}

	unconfirmed := signer
	unconfirmed.ApprovalSignature = nil
	revokedSigner := signer
	revokedSigner.RevokedAt = now
	otherTarget := target
	otherTarget.DeviceID = "tablet"
	stale := now.Add(-netproto.MaxDeviceRevocationSkew - time.Second).Unix()
	cases := map[string]func() error{
		"unconfirmed signer":    func() error { return verifyRevocationSignature("alice", unconfirmed, target, now.Unix(), good, now) },
		"revoked signer":        func() error { return verifyRevocationSignature("alice", revokedSigner, target, now.Unix(), good, now) },
		"other account":         func() error { return verifyRevocationSignature("mallory", signer, target, now.Unix(), good, now) },
		"replayed for a device": func() error { return verifyRevocationSignature("alice", signer, otherTarget, now.Unix(), good, now) },
		"stale signature": func() error {
			return verifyRevocationSignature("alice", signer, target, stale, sign("alice", stale, target), now)
		},
		"wrong time": func() error { return verifyRevocationSignature("alice", signer, target, now.Unix()+1, good, now) },
		"short":      func() error { return verifyRevocationSignature("alice", signer, target, now.Unix(), good[:32], now) },
	}
	for name, check := range cases {
		if err := check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func keyDirectoryResponse(t *testing.T, conn net.Conn, uid string) netproto.KeyResponse {
	t.Helper()
	send(t, conn, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: uid, Devices: true})
	var resp netproto.KeyResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgKeyResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSignedRevocationReachesContactsThroughTheDirectory(t *testing.T) {
	e := startDMTestEnv(t)
	userKeys, adminKeys := newLinkTestKeys(t), newLinkTestKeys(t)
	userDesktop, userDesktopID := e.desktopAs(t, "user-uid", userKeys)
	admin, _ := e.desktopAs(t, "admin-uid", adminKeys)
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, userDesktopID, userKeys)
	_, tabletID := e.approvedPhone(t, "user-uid", 2, userDesktopID, userKeys)

	signedAt := time.Now().Unix()
	sig, err := auth.SignChallenge(userKeys.private, netproto.DeviceRevocationTranscript("user-uid", userDesktopID, phoneID, phoneKeys.public, phoneKeys.x25519, signedAt, netproto.DeviceRevocationReasonUser))
	if err != nil {
		t.Fatal(err)
	}
	// A forged signature is refused outright, never downgraded.
	forged := bytes.Repeat([]byte{1}, ed25519.SignatureSize)
	if got := deviceCallError(t, userDesktop, netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: phoneID, Signature: forged, RevokedAt: signedAt}); got.Code != errCodePermissionDenied {
		t.Fatalf("forged revoke = %+v", got)
	}
	deviceCall(t, userDesktop, netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: phoneID, Signature: sig, RevokedAt: signedAt})
	// An older client's unsigned revoke still works and is listed unsigned.
	deviceCall(t, userDesktop, netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: tabletID})

	resp := keyDirectoryResponse(t, admin, "user-uid")
	if len(resp.Devices) != 1 || resp.Devices[0].DeviceID != userDesktopID {
		t.Fatalf("active devices = %+v", resp.Devices)
	}
	if len(resp.Revocations) != 2 {
		t.Fatalf("revocations = %+v", resp.Revocations)
	}
	signer, err := auth.LoadPublicKey(userKeys.public)
	if err != nil {
		t.Fatal(err)
	}
	trusted := []devicetrust.Device{{ID: userDesktopID, Signing: signer, First: true}}
	verified := devicetrust.VerifyRevocations("user-uid", trusted, resp.Revocations)
	if len(verified) != 1 || verified[0].DeviceID != phoneID || verified[0].SignerID != userDesktopID {
		t.Fatalf("verified = %+v of %+v", verified, resp.Revocations)
	}
	for _, r := range resp.Revocations {
		if r.DeviceID == tabletID && r.Signed() {
			t.Fatalf("unsigned revoke listed as signed: %+v", r)
		}
	}
}
