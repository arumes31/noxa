package server

import (
	"context"
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// Claimed keys must be usable as stored: the canonical Ed25519 spelling the
// approval signs and a usable X25519 key.
func TestDeviceLinkClaimRejectsUnusableKeys(t *testing.T) {
	env, _ := startDeviceTestEnv(t)
	desktop := desktopLogin(t, env.addr, newLinkTestKeys(t))
	defer func() { _ = desktop.Close() }()
	created := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	linkID, secret := linkSecret(t, created)

	zeroX := newLinkTestKeys(t)
	zeroX.x25519 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	phone := dialRetry(t, env.addr)
	defer func() { _ = phone.Close() }()
	if e := deviceCallError(t, phone, claimRequest(linkID, secret, zeroX)); !strings.Contains(e.Message, "encryption key") {
		t.Fatalf("zero X25519 claim error = %+v", e)
	}
	crlf := newLinkTestKeys(t)
	crlf.public = strings.ReplaceAll(crlf.public, "\n", "\r\n")
	if e := deviceCallError(t, phone, claimRequest(linkID, secret, crlf)); !strings.Contains(e.Message, "device key") {
		t.Fatalf("non-canonical key claim error = %+v", e)
	}
	// The link is still pending for a well-formed claim.
	if claimed := deviceCall(t, phone, claimRequest(linkID, secret, newLinkTestKeys(t))); claimed.State != netproto.LinkStateClaimed {
		t.Fatalf("valid claim = %+v", claimed)
	}
}

// A device approved by an unconfirmed device of the account is not vouched
// for: it is stored without an approval signature, outside DM routing.
func TestDeviceLinkFromUnconfirmedDeviceStoresNoApproval(t *testing.T) {
	env, devices := startDeviceTestEnv(t)
	first := newLinkTestKeys(t)
	if _, err := devices.AddDevice(context.Background(), store.NewDevice{
		Principal: "user-uid", Kind: store.DeviceKindDesktop, Name: "Old desktop", Ed25519Public: first.public, X25519Public: first.x25519,
	}, 3); err != nil {
		t.Fatal(err)
	}
	desktopKeys, phone := newLinkTestKeys(t), newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()
	created := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	linkID, secret := linkSecret(t, created)
	phoneConn := dialRetry(t, env.addr)
	defer func() { _ = phoneConn.Close() }()
	deviceCall(t, phoneConn, claimRequest(linkID, secret, phone))
	readOfType(t, desktop, netproto.MsgDeviceEvent)

	creator, err := devices.DeviceByKey(t.Context(), desktopKeys.public)
	if err != nil || creator.FirstDevice || len(creator.ApprovalSignature) != 0 {
		t.Fatalf("second desktop = %+v, %v", creator, err)
	}
	approval, err := auth.SignChallenge(desktopKeys.private, netproto.DeviceApprovalTranscript("user-uid", creator.DeviceID, phone.public, phone.x25519))
	if err != nil {
		t.Fatal(err)
	}
	decided := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkDecide, LinkID: linkID, Approve: true, Signature: approval})
	if decided.State != netproto.LinkStateApproved {
		t.Fatalf("decide = %+v", decided)
	}
	linked, err := devices.DeviceByKey(t.Context(), phone.public)
	if err != nil || len(linked.ApprovalSignature) != 0 || linked.ApprovedBy != creator.DeviceID {
		t.Fatalf("linked device = %+v, %v; want no approval signature", linked, err)
	}
}

// A revoked device row of another principal must not reject the sign-in of
// the account that presents the same key.
func TestSessionDeviceRejectionIsScopedToPrincipal(t *testing.T) {
	devices := &fakeDevices{}
	keys := newLinkTestKeys(t)
	d, err := devices.AddDevice(context.Background(), store.NewDevice{Principal: "other-uid", Kind: store.DeviceKindWeb, Ed25519Public: keys.public}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devices.RevokeDevice(context.Background(), "other-uid", d.DeviceID, store.DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	s := &TCPServer{deps: &Deps{Devices: devices}}
	client := &Client{authSigningKey: keys.public}
	if reason, err := s.sessionDeviceRejection(context.Background(), client, "user-uid"); err != nil || reason != "" {
		t.Fatalf("other principal's revoked row: reason=%q err=%v", reason, err)
	}
	if reason, err := s.sessionDeviceRejection(context.Background(), client, "other-uid"); err != nil || reason == "" {
		t.Fatalf("own revoked device: reason=%q err=%v, want a rejection", reason, err)
	}
}
