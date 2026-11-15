package server

import (
	"net"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// F1: once an approver is revoked, the devices it approved no longer chain to
// the first device for clients (devicetrust.Verify), so the directory and the
// per-device copy check must stop listing and requiring them.
func TestDirectoryDropsDevicesOfRevokedApprover(t *testing.T) {
	e := startDMTestEnv(t)
	desktopKeys, adminKeys := newLinkTestKeys(t), newLinkTestKeys(t)
	_, desktopID := e.desktopAs(t, "user-uid", desktopKeys)
	admin, _ := e.desktopAs(t, "admin-uid", adminKeys)
	tabletKeys, tabletID := e.approvedPhone(t, "user-uid", 2, desktopID, desktopKeys)
	_, phoneID := e.approvedPhone(t, "user-uid", 2, tabletID, tabletKeys)
	if got := keyDirectory(t, admin, "user-uid"); len(got) != 3 {
		t.Fatalf("directory before revocation = %+v", got)
	}
	if _, err := e.devices.RevokeDevice(t.Context(), "user-uid", tabletID, "idle"); err != nil {
		t.Fatal(err)
	}
	got := keyDirectory(t, admin, "user-uid")
	if len(got) != 1 || got[0].DeviceID != desktopID {
		t.Fatalf("directory after revoking %s lists %+v; %s must be gone", tabletID, got, phoneID)
	}
}

// keyGuestWithDevice signs in a key-identity guest whose key is registered
// as its first device and returns the connection and device ID.
func keyGuestWithDevice(t *testing.T, addr string, keys linkTestKeys) (net.Conn, string) {
	t.Helper()
	conn := dialRetry(t, addr)
	t.Cleanup(func() { _ = conn.Close() })
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: keys.uniqueID, Anonymous: true, Nickname: "keyguest",
		X25519PublicKey: keys.x25519, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	var ch netproto.AuthChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &ch); err != nil {
		t.Fatal(err)
	}
	sig, err := auth.SignChallenge(keys.private, ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: keys.public, Signature: sig})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil || !resp.OK {
		t.Fatalf("guest login = %+v, %v", resp, err)
	}
	for _, d := range deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionList}).Devices {
		if d.Current {
			return conn, d.DeviceID
		}
	}
	t.Skip("this server does not register key-guest devices")
	return nil, ""
}

// F15: a guest cannot spool (messages.from_user_id needs a user), so a
// per-device DM to a member with an offline device is refused before any
// copy reaches the member's online devices.
func TestGuestDeviceCopiesRefusedBeforeAnyCopyWhenRecipientOffline(t *testing.T) {
	e := startDMTestEnv(t)
	desktopKeys := newLinkTestKeys(t)
	desktop, desktopID := e.desktopAs(t, "user-uid", desktopKeys)
	_, phoneID := e.approvedPhone(t, "user-uid", 2, desktopID, desktopKeys) // never online
	guest, guestDevice := keyGuestWithDevice(t, e.env.addr, newLinkTestKeys(t))
	send(t, guest, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Enc: true, ClientMsgID: "guest-1",
		Copies: []netproto.DMCopy{{DeviceID: desktopID, Text: "ZGVza3RvcA=="}, {DeviceID: phoneID, Text: "cGhvbmU="}, {DeviceID: guestDevice, Text: "bWU="}}})
	if got := sendError(t, guest); got.Message != errGuestOffline {
		t.Fatalf("guest send = %+v", got)
	}
	if e.env.spool.pendingCount() != 0 {
		t.Fatal("guest copy spooled")
	}
	_ = desktop.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		frame, err := netproto.ReadFrame(desktop)
		if err != nil {
			break // nothing more arrived
		}
		if netproto.MessageType(frame.Type) == netproto.MsgEvent {
			if typ, _ := decodeEvent(t, frame); typ == eventChat {
				t.Fatal("the online desktop received a copy of a refused send")
			}
		}
	}
}

// F15, legacy single-copy path: refused with the same clear error.
func TestGuestLegacyDMToOfflineMemberRefused(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	guest, _ := dialGuest(t, env.addr, "guest", "")
	defer func() { _ = guest.Close() }()
	send(t, guest, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: "b3BhcXVl", Enc: true, ClientMsgID: "guest-legacy"})
	if got := sendError(t, guest); got.Message != errGuestOffline {
		t.Fatalf("guest send = %+v", got)
	}
	if env.spool.pendingCount() != 0 {
		t.Fatal("guest message spooled")
	}
}

