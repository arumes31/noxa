package main

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

// testPeerDirectory answers key directory requests for "peer" (desktop as
// first device plus a confirmed phone) and for this account "me" (this
// desktop only).
func testPeerDirectory(t *testing.T, cm func() *connManager, peerDesktop, peerPhone *testDevice) func(netproto.KeyRequest) netproto.KeyResponse {
	t.Helper()
	peerPrivate, err := auth.LoadPrivateKey(peerDesktop.edPrivate)
	if err != nil {
		t.Fatal(err)
	}
	peerRecord, err := e2ee.SignKeyRecord("peer", peerDesktop.xPublic, peerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	peerFirst := peerDesktop.deviceInfo
	peerFirst.FirstDevice = true
	phone := peerPhone.approve(t, "peer", peerDesktop)
	return func(r netproto.KeyRequest) netproto.KeyResponse {
		resp := netproto.KeyResponse{UniqueID: r.UniqueID}
		switch r.UniqueID {
		case "peer":
			resp.PublicKey = peerRecord
			if r.Devices {
				resp.Devices = []netproto.DMDevice{peerFirst, phone}
			}
		case "me":
			id, _ := cm().identity()
			pub, _, _ := id.x25519()
			resp.Devices = []netproto.DMDevice{{DeviceID: "self", Kind: "desktop", Ed25519Public: id.PublicKey,
				X25519Public: base64.StdEncoding.EncodeToString(pub[:]), FirstDevice: true}}
		}
		return resp
	}
}

func TestEditAndDeleteDirectMessageSendDeviceCopies(t *testing.T) {
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
	var mu sync.Mutex
	var sends []netproto.ChatSend
	var cm *connManager
	directory := testPeerDirectory(t, func() *connManager { return cm }, peerDesktop, peerPhone)
	var app *App
	app, cm = newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		mu.Lock()
		defer mu.Unlock()
		switch netproto.MessageType(f.Type) {
		case netproto.MsgKeyRequest:
			var r netproto.KeyRequest
			_ = netproto.Decode(f, &r)
			return netproto.MsgKeyResponse, directory(r), true
		case netproto.MsgChatSend:
			var msg netproto.ChatSend
			_ = netproto.Decode(f, &msg)
			sends = append(sends, msg)
			if len(sends) == 1 {
				return netproto.MsgError, netproto.Error{Code: 409, OriginType: uint16(netproto.MsgChatSend), RecipientDevicesStale: true, Message: "stale"}, true
			}
			return netproto.MsgChatAccepted, netproto.ChatAccepted{ClientMsgID: msg.ClientMsgID, ToUniqueID: msg.ToUniqueID, Disposition: netproto.ChatQueued}, true
		}
		return 0, nil, false
	})
	cm.mu.Lock()
	cm.uniqueID, cm.deviceID, cm.supportsDMDevices = "me", "self", true
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	app.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if msg := app.DMHistoryAppend("peer", "Peer", DMEntry{FromUniqueID: "me", Body: "hello", Self: true, ClientMsgID: "orig", EncVerified: true, EncMode: "e2e"}); msg != "" {
		t.Fatal(msg)
	}
	if msg := app.DMHistoryAppend("peer", "Peer", DMEntry{FromUniqueID: "peer", Body: "theirs", ClientMsgID: "theirs", EncVerified: true, EncMode: "e2e"}); msg != "" {
		t.Fatal(msg)
	}

	if err := app.EditDirectMessageForTab("a", "peer", "theirs", "mine now"); !errors.Is(err, errDMChangeNotOwn) {
		t.Fatalf("editing another sender's message: %v", err)
	}
	if err := app.EditDirectMessageForTab("a", "peer", "orig", "hello again"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := app.DeleteDirectMessageForTab("a", "peer", "orig"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	mu.Lock()
	got := append([]netproto.ChatSend(nil), sends...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("sends = %d", len(got))
	}
	ownPublic, ownPrivate, _ := cm.id.x25519()
	keys := map[string][32]byte{"peer-desktop": peerDesktop.xPrivate, "peer-phone": peerPhone.xPrivate, "self": ownPrivate}
	check := func(msg netproto.ChatSend, action, want string) {
		t.Helper()
		if msg.DMAction != action || msg.RefClientMsgID != "orig" || msg.ClientMsgID == "" || msg.ClientMsgID == "orig" {
			t.Fatalf("change = %q ref %q id %q", msg.DMAction, msg.RefClientMsgID, msg.ClientMsgID)
		}
		if msg.Text != "" || msg.RecipientPublicKey != "" || len(msg.Copies) != len(keys) {
			t.Fatalf("%s carries %d copies", action, len(msg.Copies))
		}
		for _, c := range msg.Copies {
			if got := openCopy(t, c.Text, ownPublic, keys[c.DeviceID]); got != want {
				t.Fatalf("%s copy for %s = %q", action, c.DeviceID, got)
			}
		}
	}
	// The stale-device retry keeps the change and its ClientMsgID.
	check(got[0], netproto.DMActionEdit, "hello again")
	check(got[1], netproto.DMActionEdit, "hello again")
	if got[0].ClientMsgID != got[1].ClientMsgID {
		t.Fatal("retry changed the ClientMsgID")
	}
	check(got[2], netproto.DMActionDelete, "")

	// Without per-device mode a change cannot be sent.
	cm.mu.Lock()
	cm.supportsDMDevices = false
	cm.mu.Unlock()
	if err := app.EditDirectMessageForTab("a", "peer", "orig", "x"); !errors.Is(err, errDMChangeUnsupported) {
		t.Fatalf("legacy edit: %v", err)
	}
}

// dmChangeReceiver is an App with local history and a device-registered
// manager whose received changes persist like a tab manager's.
func dmChangeReceiver(t *testing.T) (*App, *connManager, [32]byte, [32]byte, *testDevice) {
	t.Helper()
	cm, pub, priv := deviceCopyManager(t)
	app := appWithCM(cm)
	app.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	cm.dmChanges = app.recordDMChange
	peerPhone := newTestDevice(t, "peer-phone", "web")
	cm.dmDevices.put("peer", []dmTrustedDevice{{DeviceID: peerPhone.id, X25519: peerPhone.xPublic}}, 0, time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub, First: true}}, 0, time.Now())
	return app, cm, pub, priv, peerPhone
}

func changeCopy(t *testing.T, sender *testDevice, to [32]byte, action, ref, text string) netproto.ChatBroadcast {
	t.Helper()
	blob, err := sealDM(text, to, sender.xPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return netproto.ChatBroadcast{
		Direct: true, Enc: true, E2E: true, FromUniqueID: "peer", ToUniqueID: "me", Text: blob, ClientMsgID: "change-" + action,
		SenderDeviceID: sender.id, SenderDeviceKey: sender.deviceInfo.X25519Public, RecipientDeviceID: "self",
		DMAction: action, RefClientMsgID: ref,
	}
}

func loadPeerLog(t *testing.T, app *App) map[string]DMEntry {
	t.Helper()
	entries, err := app.DMHistoryLoad("peer")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]DMEntry{}
	for _, e := range entries {
		out[e.ClientMsgID] = e
	}
	return out
}

func TestReceivedDirectMessageChangesApplyOnlyFromTheSender(t *testing.T) {
	app, cm, pub, priv, peerPhone := dmChangeReceiver(t)
	for _, e := range []DMEntry{
		{FromUniqueID: "peer", Body: "original", ClientMsgID: "m1", EncVerified: true, EncMode: "e2e"},
		{FromUniqueID: "me", Body: "my own", Self: true, ClientMsgID: "m2", EncVerified: true, EncMode: "e2e"},
	} {
		if msg := app.DMHistoryAppend("peer", "Peer", e); msg != "" {
			t.Fatal(msg)
		}
	}

	cm.decryptDMAsync(changeCopy(t, peerPhone, pub, netproto.DMActionEdit, "m1", "edited body"), "")
	// The peer cannot edit a message this account sent.
	cm.decryptDMAsync(changeCopy(t, peerPhone, pub, netproto.DMActionEdit, "m2", "hijacked"), "")
	// A copy from a device the peer never confirmed is not opened at all.
	stranger := newTestDevice(t, "stranger", "web")
	cm.dmDevices.put("peer", []dmTrustedDevice{{DeviceID: peerPhone.id, X25519: peerPhone.xPublic}}, 0, time.Now().Add(time.Hour))
	forged := changeCopy(t, stranger, pub, netproto.DMActionDelete, "m1", "")
	if _, ok := cm.openDeviceCopy(forged, pub, priv); ok {
		t.Fatal("copy from an unconfirmed device opened")
	}

	log := loadPeerLog(t, app)
	if e := log["m1"]; e.Body != "edited body" || !e.Edited || e.EditedAt == 0 || e.Deleted {
		t.Fatalf("edited entry = %+v", e)
	}
	if e := log["m2"]; e.Body != "my own" || e.Edited {
		t.Fatalf("foreign edit applied: %+v", e)
	}
	if len(log) != 2 {
		t.Fatalf("a change became a message: %d entries", len(log))
	}

	cm.decryptDMAsync(changeCopy(t, peerPhone, pub, netproto.DMActionDelete, "m1", ""), "")
	if e := loadPeerLog(t, app)["m1"]; !e.Deleted || e.Body != "" {
		t.Fatalf("deleted entry = %+v", e)
	}
	// A deletion is final.
	cm.decryptDMAsync(changeCopy(t, peerPhone, pub, netproto.DMActionEdit, "m1", "back"), "")
	if e := loadPeerLog(t, app)["m1"]; !e.Deleted || e.Body != "" {
		t.Fatalf("edit revived a deleted entry: %+v", e)
	}

	rec := cm.sink.(*eventRecorder)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	changed := 0
	for _, e := range rec.events {
		if strings.Contains(e.payload, `"type":"chat"`) {
			t.Fatalf("a change was emitted as a chat message: %s", e.payload)
		}
		if strings.Contains(e.payload, `"type":"dm_changed"`) {
			changed++
		}
	}
	if changed != 4 {
		t.Fatalf("dm_changed events = %d", changed)
	}
}

func TestDirectMessageChangeWaitsForItsOriginal(t *testing.T) {
	app, cm, pub, _, peerPhone := dmChangeReceiver(t)
	cm.decryptDMAsync(changeCopy(t, peerPhone, pub, netproto.DMActionEdit, "late", "final text"), "")
	if log := loadPeerLog(t, app); len(log) != 0 {
		t.Fatalf("a change created an entry: %+v", log)
	}
	// Another sender's message under the same ClientMsgID is not changed.
	store, err := app.captureDMHistory()
	if err != nil {
		t.Fatal(err)
	}
	app.dmPending.add(store.path("peer"), dmChange{Action: netproto.DMActionDelete, Ref: "late", From: "mallory"}, time.Now())
	if msg := app.DMHistoryAppend("peer", "Peer", DMEntry{FromUniqueID: "peer", Body: "first text", ClientMsgID: "late", EncVerified: true, EncMode: "e2e"}); msg != "" {
		t.Fatal(msg)
	}
	e := loadPeerLog(t, app)["late"]
	if e.Body != "final text" || !e.Edited || e.Deleted {
		t.Fatalf("pending change not applied: %+v", e)
	}
	if applied, err := app.dmHistoryApplyChangeWith(store, "peer", dmChange{Action: netproto.DMActionDelete, Ref: "late", From: "mallory"}); applied || err != nil {
		t.Fatalf("foreign delete applied=%v err=%v", applied, err)
	}
}

func TestDMHistoryChangeFieldsRoundTrip(t *testing.T) {
	app, _, _, _, _ := dmChangeReceiver(t)
	in := DMEntry{FromUniqueID: "peer", Body: "", ClientMsgID: "x", SentAt: 5, Edited: true, EditedAt: 7, Deleted: true, EncVerified: true, EncMode: "e2e"}
	if msg := app.DMHistoryAppend("peer", "Peer", in); msg != "" {
		t.Fatal(msg)
	}
	out := loadPeerLog(t, app)["x"]
	if !out.Edited || out.EditedAt != 7 || !out.Deleted || out.Body != "" {
		t.Fatalf("round trip = %+v", out)
	}
	// Deleted entries never match a search.
	res, err := app.DMSearch("peer", "", 10)
	if err != nil || len(res.Messages) != 0 {
		t.Fatalf("search = %+v, %v", res.Messages, err)
	}
}
