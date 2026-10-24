package server

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// fakeDeviceMessages adds endorsements and history copies to fakePushes.
type fakeDeviceMessages struct {
	*fakePushes
	hmu     sync.Mutex
	history map[string][]store.HistoryChunk
}

func (f *fakeDeviceMessages) EndorseDevice(_ context.Context, principal, deviceID, approverID string, signature []byte) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, d := range f.devices {
		if d.Principal == principal && d.DeviceID == deviceID && d.Active() && !d.FirstDevice && len(d.ApprovalSignature) == 0 {
			f.devices[i].ApprovedBy, f.devices[i].ApprovalSignature = approverID, signature
			return f.devices[i], nil
		}
	}
	return store.Device{}, store.ErrDeviceNotFound
}

func (f *fakeDeviceMessages) PutHistoryChunk(_ context.Context, _ string, c store.HistoryChunk, maxBytes int64) error {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	if f.history == nil {
		f.history = map[string][]store.HistoryChunk{}
	}
	if c.Seq == 0 {
		f.history[c.DeviceID] = nil
	}
	var total int64
	for _, existing := range f.history[c.DeviceID] {
		total += int64(len(existing.Data))
	}
	if total+int64(len(c.Data)) > maxBytes {
		return store.ErrHistoryLimit
	}
	f.history[c.DeviceID] = append(f.history[c.DeviceID], c)
	return nil
}

func (f *fakeDeviceMessages) HistoryChunks(_ context.Context, _ string, deviceID string, from, limit int) ([]store.HistoryChunk, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	var out []store.HistoryChunk
	for _, c := range f.history[deviceID] {
		if c.Seq >= from && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeDeviceMessages) AckHistoryChunks(_ context.Context, _ string, deviceID string, through int) error {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	keep := f.history[deviceID][:0]
	for _, c := range f.history[deviceID] {
		if c.Seq > through {
			keep = append(keep, c)
		}
	}
	f.history[deviceID] = keep
	return nil
}

func (f *fakeDeviceMessages) ExpireHistoryChunks(context.Context, time.Time) (int64, error) {
	return 0, nil
}

type dmTestEnv struct {
	env     *testEnv
	devices *fakeDeviceMessages
}

func startDMTestEnv(t *testing.T) dmTestEnv {
	t.Helper()
	devices := &fakeDeviceMessages{fakePushes: &fakePushes{fakeDevices: &fakeDevices{}}}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example"}
	}, func(deps *Deps) { deps.Devices = devices })
	t.Cleanup(env.stop)
	return dmTestEnv{env: env, devices: devices}
}

// desktopAs signs uid in with its password and identity key and registers
// the desktop as the account's first device.
func (e dmTestEnv) desktopAs(t *testing.T, uid string, keys linkTestKeys) (net.Conn, string) {
	t.Helper()
	conn := dialRetry(t, e.env.addr)
	t.Cleanup(func() { _ = conn.Close() })
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: uid, Password: "pw", PublicKey: keys.public, X25519PublicKey: keys.x25519})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil || !resp.OK {
		t.Fatalf("%s login = %+v, %v", uid, resp, err)
	}
	listed := deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	for _, d := range listed.Devices {
		if d.Current {
			return conn, d.DeviceID
		}
	}
	t.Fatalf("%s desktop not registered: %+v", uid, listed.Devices)
	return nil, ""
}

// approvedPhone links a web device approved by the desktop's key.
func (e dmTestEnv) approvedPhone(t *testing.T, uid string, userID int64, approverID string, approver linkTestKeys) (linkTestKeys, string) {
	t.Helper()
	phone := newLinkTestKeys(t)
	signature, err := auth.SignChallenge(approver.private, netproto.DeviceApprovalTranscript(uid, approverID, phone.public, phone.x25519))
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.devices.AddDevice(t.Context(), store.NewDevice{Principal: uid, UserID: userID, Kind: store.DeviceKindWeb, Name: "Pixel",
		Ed25519Public: phone.public, X25519Public: phone.x25519, ApprovedBy: approverID, ApprovalSignature: signature}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return phone, d.DeviceID
}

func keyDirectory(t *testing.T, conn net.Conn, uid string) []netproto.DMDevice {
	t.Helper()
	send(t, conn, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: uid, Devices: true})
	var resp netproto.KeyResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgKeyResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Devices
}

func chatEvent(t *testing.T, conn net.Conn) netproto.ChatBroadcast {
	t.Helper()
	var chat netproto.ChatBroadcast
	if err := json.Unmarshal(readEventOfType(t, conn, eventChat), &chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

func sendError(t *testing.T, conn net.Conn) netproto.Error {
	t.Helper()
	var e netproto.Error
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDirectMessagesReachEveryTrustedDevice(t *testing.T) {
	e := startDMTestEnv(t)
	userDesktopKeys, adminKeys := newLinkTestKeys(t), newLinkTestKeys(t)
	userDesktop, userDesktopID := e.desktopAs(t, "user-uid", userDesktopKeys)
	admin, adminDeviceID := e.desktopAs(t, "admin-uid", adminKeys)
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, userDesktopID, userDesktopKeys)

	// A passkey-linked phone without an approval is not listed.
	passkeyPhone := newLinkTestKeys(t)
	unendorsed, err := e.devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", UserID: 2, Kind: store.DeviceKindWeb, Name: "Tablet", Ed25519Public: passkeyPhone.public, X25519Public: passkeyPhone.x25519}, 10)
	if err != nil {
		t.Fatal(err)
	}
	listed := keyDirectory(t, admin, "user-uid")
	if len(listed) != 2 || listed[0].DeviceID != userDesktopID || !listed[0].FirstDevice || listed[1].DeviceID != phoneID || len(listed[1].ApprovalSignature) == 0 {
		t.Fatalf("directory = %+v", listed)
	}

	// A single account-wide copy cannot reach the phone.
	send(t, admin, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Text: "c2VhbGVk", Enc: true, ClientMsgID: "legacy-1"})
	if got := sendError(t, admin); !got.RecipientDevicesStale {
		t.Fatalf("legacy send = %+v", got)
	}
	// Copies must cover every trusted device of the recipient.
	send(t, admin, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Enc: true, ClientMsgID: "partial-1",
		Copies: []netproto.DMCopy{{DeviceID: userDesktopID, Text: "Zm9y"}}})
	if got := sendError(t, admin); !got.RecipientDevicesStale {
		t.Fatalf("partial send = %+v", got)
	}

	send(t, admin, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Enc: true, AckRequested: true, ClientMsgID: "dm-1",
		Copies: []netproto.DMCopy{{DeviceID: userDesktopID, Text: "Zm9yLWRlc2t0b3A="}, {DeviceID: phoneID, Text: "Zm9yLXBob25l"}, {DeviceID: adminDeviceID, Text: "Zm9yLW1l"}}})
	echo := chatEvent(t, admin)
	if echo.Text != "Zm9yLW1l" || echo.RecipientDeviceID != adminDeviceID {
		t.Fatalf("own copy = %+v", echo)
	}
	got := chatEvent(t, userDesktop)
	if got.Text != "Zm9yLWRlc2t0b3A=" || got.SenderDeviceID != adminDeviceID || got.SenderDeviceKey != adminKeys.x25519 ||
		got.RecipientDeviceID != userDesktopID || got.FromUniqueID != "admin-uid" || !got.Direct {
		t.Fatalf("desktop copy = %+v", got)
	}

	// The offline phone receives only its own copy when it signs in.
	phone, resp := keyLogin(t, e.env.addr, phoneKeys)
	defer func() { _ = phone.Close() }()
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	spooled := chatEvent(t, phone)
	if spooled.Text != "Zm9yLXBob25l" || !spooled.Offline || spooled.RecipientDeviceID != phoneID || spooled.SenderDeviceKey != adminKeys.x25519 {
		t.Fatalf("spooled phone copy = %+v", spooled)
	}

	// The desktop confirms the passkey phone; it then counts as a device.
	signature, err := auth.SignChallenge(userDesktopKeys.private, netproto.DeviceApprovalTranscript("user-uid", userDesktopID, passkeyPhone.public, passkeyPhone.x25519))
	if err != nil {
		t.Fatal(err)
	}
	if e := deviceCallError(t, userDesktop, netproto.DeviceRequest{Action: netproto.DeviceActionEndorse, DeviceID: unendorsed.DeviceID, Signature: []byte("forged")}); e.Code != errCodePermissionDenied {
		t.Fatalf("forged endorsement = %+v", e)
	}
	endorsed := deviceCall(t, userDesktop, netproto.DeviceRequest{Action: netproto.DeviceActionEndorse, DeviceID: unendorsed.DeviceID, Signature: signature})
	if endorsed.Device == nil || endorsed.Device.ApprovedBy != userDesktopID {
		t.Fatalf("endorse = %+v", endorsed)
	}
	if listed := keyDirectory(t, admin, "user-uid"); len(listed) != 3 {
		t.Fatalf("directory after endorsement = %+v", listed)
	}
}

func TestHistoryCopyReachesTheNewDevice(t *testing.T) {
	e := startDMTestEnv(t)
	desktopKeys := newLinkTestKeys(t)
	desktop, desktopID := e.desktopAs(t, "user-uid", desktopKeys)
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, desktopID, desktopKeys)
	phone, resp := keyLogin(t, e.env.addr, phoneKeys)
	defer func() { _ = phone.Close() }()
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}

	if e := deviceCallError(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionHistoryPut, DeviceID: phoneID,
		History: &netproto.HistoryChunk{Seq: 0, Total: 1, Data: []byte("x")}}); e.Code != errCodeMalformed {
		t.Fatalf("history to itself = %+v", e)
	}
	for seq, data := range []string{"part-one", "part-two"} {
		deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionHistoryPut, DeviceID: phoneID,
			History: &netproto.HistoryChunk{Seq: seq, Total: 2, Data: []byte(data)}})
	}
	var ready netproto.DeviceEvent
	for ready.Event != netproto.DeviceEventHistoryReady {
		if err := netproto.Decode(readOfType(t, phone, netproto.MsgDeviceEvent), &ready); err != nil {
			t.Fatal(err)
		}
	}
	if ready.HistoryTotal != 2 {
		t.Fatalf("ready = %+v", ready)
	}
	got := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionHistoryGet})
	if len(got.History) != 2 || string(got.History[1].Data) != "part-two" || got.History[0].FromDeviceID != desktopID {
		t.Fatalf("history = %+v", got.History)
	}
	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionHistoryAck, Seq: 1})
	if left := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionHistoryGet}); len(left.History) != 0 {
		t.Fatalf("history after ack = %+v", left.History)
	}
}

func TestPhonesDoNotReplaceTheAccountKey(t *testing.T) {
	e := startDMTestEnv(t)
	desktopKeys := newLinkTestKeys(t)
	_, desktopID := e.desktopAs(t, "user-uid", desktopKeys)
	phoneKeys, _ := e.approvedPhone(t, "user-uid", 2, desktopID, desktopKeys)
	phone, serverSide := net.Pipe()
	t.Cleanup(func() { _ = phone.Close() })
	go func() { _ = e.env.srv.ServeExternalConn(context.Background(), webPipe{serverSide}) }()
	if resp := keyLoginOn(t, phone, phoneKeys); !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	go func() {
		for {
			if _, err := netproto.ReadFrame(phone); err != nil {
				return
			}
		}
	}()
	before, _ := e.env.auth.GetE2EPublicKey(t.Context(), "user-uid")
	frame, err := netproto.Encode(netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: phoneKeys.x25519})
	if err != nil {
		t.Fatal(err)
	}
	if err := netproto.WriteFrame(phone, frame); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if after, _ := e.env.auth.GetE2EPublicKey(t.Context(), "user-uid"); after != before {
		t.Fatalf("phone replaced the account key: %q -> %q", before, after)
	}
}

func TestDirectMessageEditsTravelLikeMessages(t *testing.T) {
	e := startDMTestEnv(t)
	userDesktopKeys, adminKeys := newLinkTestKeys(t), newLinkTestKeys(t)
	userDesktop, userDesktopID := e.desktopAs(t, "user-uid", userDesktopKeys)
	admin, _ := e.desktopAs(t, "admin-uid", adminKeys)
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, userDesktopID, userDesktopKeys)

	// A change without a reference, or on a single account-wide copy, is refused.
	send(t, admin, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Enc: true, ClientMsgID: "bad-1", DMAction: netproto.DMActionEdit,
		Copies: []netproto.DMCopy{{DeviceID: userDesktopID, Text: "eA=="}, {DeviceID: phoneID, Text: "eA=="}}})
	if got := sendError(t, admin); got.Code != errCodeMalformed {
		t.Fatalf("edit without reference = %+v", got)
	}

	send(t, admin, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "user-uid", Enc: true, AckRequested: true, ClientMsgID: "edit-1",
		DMAction: netproto.DMActionEdit, RefClientMsgID: "dm-1",
		Copies: []netproto.DMCopy{{DeviceID: userDesktopID, Text: "bmV3"}, {DeviceID: phoneID, Text: "bmV3LXBob25l"}}})
	live := chatEvent(t, userDesktop)
	if live.DMAction != netproto.DMActionEdit || live.RefClientMsgID != "dm-1" || live.ClientMsgID != "edit-1" {
		t.Fatalf("live edit = %+v", live)
	}
	phone, resp := keyLogin(t, e.env.addr, phoneKeys)
	defer func() { _ = phone.Close() }()
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	spooled := chatEvent(t, phone)
	if spooled.DMAction != netproto.DMActionEdit || spooled.RefClientMsgID != "dm-1" || spooled.Text != "bmV3LXBob25l" {
		t.Fatalf("spooled edit = %+v", spooled)
	}
}

func TestOwnDeviceCopiesKeepTheirContact(t *testing.T) {
	e := startDMTestEnv(t)
	userDesktopKeys := newLinkTestKeys(t)
	userDesktop, userDesktopID := e.desktopAs(t, "user-uid", userDesktopKeys)
	admin, adminDeviceID := e.desktopAs(t, "admin-uid", newLinkTestKeys(t))
	defer func() { _ = admin.Close() }()
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, userDesktopID, userDesktopKeys)

	// The desktop writes to admin; its offline phone gets a copy too.
	send(t, userDesktop, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "admin-uid", Enc: true, AckRequested: true, ClientMsgID: "own-1",
		Copies: []netproto.DMCopy{{DeviceID: adminDeviceID, Text: "YWRtaW4="}, {DeviceID: phoneID, Text: "cGhvbmU="}}})
	readOfType(t, userDesktop, netproto.MsgChatAccepted)
	phone, resp := keyLogin(t, e.env.addr, phoneKeys)
	defer func() { _ = phone.Close() }()
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	spooled := chatEvent(t, phone)
	if spooled.FromUniqueID != "user-uid" || spooled.ToUniqueID != "admin-uid" || spooled.Text != "cGhvbmU=" {
		t.Fatalf("own copy from the spool = %+v", spooled)
	}
}
