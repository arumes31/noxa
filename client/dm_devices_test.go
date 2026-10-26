package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

// testDevice is one device's key material as a test directory lists it.
type testDevice struct {
	id         string
	edPublic   string // auth text format
	edPrivate  string
	xPublic    [32]byte
	xPrivate   [32]byte
	deviceInfo netproto.DMDevice
}

func newTestDevice(t *testing.T, id, kind string) *testDevice {
	t.Helper()
	edPublic, edPrivate, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	xPublic, xPrivate, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := &testDevice{id: id, edPublic: edPublic, edPrivate: edPrivate, xPublic: *xPublic, xPrivate: *xPrivate}
	d.deviceInfo = netproto.DMDevice{DeviceID: id, Kind: kind, Ed25519Public: edPublic, X25519Public: base64.StdEncoding.EncodeToString(xPublic[:])}
	return d
}

func (d *testDevice) signingKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	key, err := auth.LoadPublicKey(d.edPublic)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// approve makes approver sign d into principal's device list.
func (d *testDevice) approve(t *testing.T, principal string, approver *testDevice) netproto.DMDevice {
	t.Helper()
	transcript := netproto.DeviceApprovalTranscript(principal, approver.id, d.deviceInfo.Ed25519Public, d.deviceInfo.X25519Public)
	signature, err := auth.SignChallenge(approver.edPrivate, transcript)
	if err != nil {
		t.Fatal(err)
	}
	out := d.deviceInfo
	out.ApprovedBy, out.ApprovalSignature = approver.id, signature
	return out
}

func trustedIDs(devices []dmTrustedDevice) string {
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.DeviceID)
	}
	return strings.Join(ids, ",")
}

func TestVerifyDMDevicesFollowsTheApprovalChain(t *testing.T) {
	const principal = "alice"
	desktop := newTestDevice(t, "desktop", "desktop")
	phone := newTestDevice(t, "phone", "web")
	tablet := newTestDevice(t, "tablet", "web")
	forged := newTestDevice(t, "forged", "web")
	stranger := newTestDevice(t, "stranger", "desktop")
	cycleA := newTestDevice(t, "cycle-a", "web")
	cycleB := newTestDevice(t, "cycle-b", "web")
	unsigned := newTestDevice(t, "passkey", "web")

	first := desktop.deviceInfo
	first.FirstDevice = true
	// A forged approval claims the desktop but is signed by another key.
	forgedEntry := forged.approve(t, principal, stranger)
	forgedEntry.ApprovedBy = desktop.id
	// A device approved by a phone signed over another principal.
	wrongPrincipal := tablet.approve(t, "mallory", phone)
	wrongPrincipal.DeviceID = "wrong-principal"
	// Passkey login: names its registering device but carries no signature.
	passkey := unsigned.deviceInfo
	passkey.ApprovedBy = desktop.id
	devices := []netproto.DMDevice{
		tablet.approve(t, principal, phone), // listed before its approver
		first,
		phone.approve(t, principal, desktop),
		forgedEntry,
		wrongPrincipal,
		cycleA.approve(t, principal, cycleB),
		cycleB.approve(t, principal, cycleA),
		passkey,
	}
	trusted, err := verifyDMDevices(principal, desktop.signingKey(t), desktop.xPublic, devices)
	if err != nil {
		t.Fatal(err)
	}
	if got := trustedIDs(trusted); got != "desktop,tablet,phone" {
		t.Fatalf("trusted = %q", got)
	}
	for _, d := range trusted {
		want := map[string][32]byte{"desktop": desktop.xPublic, "phone": phone.xPublic, "tablet": tablet.xPublic}[d.DeviceID]
		if d.X25519 != want {
			t.Fatalf("%s carries the wrong encryption key", d.DeviceID)
		}
	}
}

func TestVerifyDMDevicesRefusesAnotherFirstDevice(t *testing.T) {
	const principal = "alice"
	desktop := newTestDevice(t, "desktop", "desktop")
	impostor := newTestDevice(t, "impostor", "desktop")
	phone := newTestDevice(t, "phone", "web")
	first := impostor.deviceInfo
	first.FirstDevice = true
	devices := []netproto.DMDevice{first, phone.approve(t, principal, impostor)}
	if _, err := verifyDMDevices(principal, desktop.signingKey(t), desktop.xPublic, devices); err != errDMDevicesUnverified {
		t.Fatalf("impostor first device err = %v", err)
	}
	// No first device at all, or two of them, is refused as well.
	if _, err := verifyDMDevices(principal, desktop.signingKey(t), desktop.xPublic, devices[1:]); err != errDMDevicesUnverified {
		t.Fatalf("missing first device err = %v", err)
	}
	own := desktop.deviceInfo
	own.FirstDevice = true
	if _, err := verifyDMDevices(principal, desktop.signingKey(t), desktop.xPublic, []netproto.DMDevice{own, first}); err != errDMDevicesUnverified {
		t.Fatalf("two first devices err = %v", err)
	}
}

func TestVerifyDMDevicesRejectsDirectoryWhenFirstEncryptionKeyDisagrees(t *testing.T) {
	const principal = "alice"
	desktop := newTestDevice(t, "desktop", "desktop")
	phone := newTestDevice(t, "phone", "web")
	other := newTestDevice(t, "other", "web")
	first := desktop.deviceInfo
	first.FirstDevice = true
	first.X25519Public = other.deviceInfo.X25519Public
	trusted, err := verifyDMDevices(principal, desktop.signingKey(t), desktop.xPublic, []netproto.DMDevice{first, phone.approve(t, principal, desktop)})
	if err != errDMDevicesUnverified || len(trusted) != 0 {
		t.Fatalf("trusted = %+v, error = %v", trusted, err)
	}

}

func openCopy(t *testing.T, blob string, sender, recipient [32]byte) string {
	t.Helper()
	plain, err := openDM(blob, sender, recipient)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func TestBuildDMCopiesSealsForEveryDevice(t *testing.T) {
	self := newTestDevice(t, "self", "desktop")
	ownPhone := newTestDevice(t, "own-phone", "web")
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
	trusted := func(devices ...*testDevice) []dmTrustedDevice {
		out := make([]dmTrustedDevice, 0, len(devices))
		for _, d := range devices {
			out = append(out, dmTrustedDevice{DeviceID: d.id, X25519: d.xPublic})
		}
		return out
	}
	copies, err := buildDMCopies("hello", self.xPrivate, trusted(peerDesktop, peerPhone), trusted(ownPhone, self), self.id)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*testDevice{self.id: self, ownPhone.id: ownPhone, peerDesktop.id: peerDesktop, peerPhone.id: peerPhone}
	if len(copies) != len(byID) {
		t.Fatalf("copies = %d", len(copies))
	}
	for _, c := range copies {
		d := byID[c.DeviceID]
		if d == nil {
			t.Fatalf("copy for unknown device %q", c.DeviceID)
		}
		delete(byID, c.DeviceID)
		if got := openCopy(t, c.Text, self.xPublic, d.xPrivate); got != "hello" {
			t.Fatalf("%s opened %q", c.DeviceID, got)
		}
	}
	if _, err := buildDMCopies("hello", self.xPrivate, nil, trusted(self), self.id); err == nil {
		t.Fatal("copies without a recipient device")
	}
}

// deviceCopyManager is a manager registered as device "self" of account "me".
func deviceCopyManager(t *testing.T) (*connManager, [32]byte, [32]byte) {
	t.Helper()
	cm := newConnManager(context.TODO())
	cm.sink = &eventRecorder{}
	cm.addr = "test.invalid:9987"
	cm.confirmPeerChange = nil
	cm.id = mustTempIdentity(t)
	cm.uniqueID, cm.deviceID, cm.supportsDMDevices = "me", "self", true
	pub, priv, err := cm.id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	return cm, pub, priv
}

func TestDecryptDeviceCopyRequiresATrustedSenderDevice(t *testing.T) {
	cm, pub, priv := deviceCopyManager(t)
	peerPhone := newTestDevice(t, "peer-phone", "web")
	ownPhone := newTestDevice(t, "own-phone", "web")
	unknown := newTestDevice(t, "unknown", "web")
	cm.dmDevices.put("peer", []dmTrustedDevice{{DeviceID: peerPhone.id, X25519: peerPhone.xPublic}}, 0, time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub}, {DeviceID: ownPhone.id, X25519: ownPhone.xPublic}}, 0, time.Now())

	seal := func(text string, sender *testDevice) string {
		blob, err := sealDM(text, pub, sender.xPrivate)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	copyFrom := func(from string, sender *testDevice, text string) netproto.ChatBroadcast {
		return netproto.ChatBroadcast{
			Direct: true, Enc: true, E2E: true, FromUniqueID: from, ToUniqueID: "peer", Text: seal(text, sender),
			SenderDeviceID: sender.id, SenderDeviceKey: sender.deviceInfo.X25519Public, RecipientDeviceID: "self",
		}
	}
	incoming := copyFrom("peer", peerPhone, "from their phone")
	incoming.ToUniqueID = "me"
	if got := cm.decryptDeviceCopy(incoming, pub, priv); got != "from their phone" {
		t.Fatalf("peer copy = %q", got)
	}
	// A message this user sent from their phone arrives as their own.
	if got := cm.decryptDeviceCopy(copyFrom("me", ownPhone, "sent on my phone"), pub, priv); got != "sent on my phone" {
		t.Fatalf("own phone copy = %q", got)
	}
	// This device's own echo is sealed from and to its own key.
	echo, err := sealDM("my echo", pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	self := netproto.ChatBroadcast{FromUniqueID: "me", ToUniqueID: "peer", Text: echo, SenderDeviceID: "self",
		SenderDeviceKey: base64.StdEncoding.EncodeToString(pub[:]), RecipientDeviceID: "self"}
	if got := cm.decryptDeviceCopy(self, pub, priv); got != "my echo" {
		t.Fatalf("echo = %q", got)
	}

	// An unknown device, a key swapped under a trusted id, and a copy for
	// another device all stay unopened.
	if got := cm.decryptDeviceCopy(copyFrom("peer", unknown, "spoofed"), pub, priv); got != missingKeyText {
		t.Fatalf("unknown device copy = %q", got)
	}
	swapped := copyFrom("peer", unknown, "swapped")
	swapped.SenderDeviceID = peerPhone.id
	if got := cm.decryptDeviceCopy(swapped, pub, priv); got != missingKeyText {
		t.Fatalf("swapped key copy = %q", got)
	}
	elsewhere := copyFrom("peer", peerPhone, "not mine")
	elsewhere.RecipientDeviceID = "other"
	if got := cm.decryptDeviceCopy(elsewhere, pub, priv); got != missingKeyText {
		t.Fatalf("foreign copy = %q", got)
	}
}

// TestDirectMessageSendsCopiesAndRetriesStaleDevices drives encryptChat and
// the send retry against a piped directory: the first send is refused as
// stale, the retry carries the new phone and the same ClientMsgID.
func TestDirectMessageSendsCopiesAndRetriesStaleDevices(t *testing.T) {
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
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

	var mu sync.Mutex
	var sends []netproto.ChatSend
	phoneLinked := false
	var cm *connManager
	_, cm = newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		mu.Lock()
		defer mu.Unlock()
		switch netproto.MessageType(f.Type) {
		case netproto.MsgKeyRequest:
			var r netproto.KeyRequest
			_ = netproto.Decode(f, &r)
			resp := netproto.KeyResponse{UniqueID: r.UniqueID}
			switch r.UniqueID {
			case "peer":
				resp.PublicKey = peerRecord
				if r.Devices {
					resp.Devices = []netproto.DMDevice{peerFirst}
					if phoneLinked {
						resp.Devices = append(resp.Devices, peerPhone.approve(t, "peer", peerDesktop))
					}
				}
			case "me":
				id, _ := cm.identity()
				pub, _, _ := id.x25519()
				resp.Devices = []netproto.DMDevice{{DeviceID: "self", Kind: "desktop", Ed25519Public: id.PublicKey,
					X25519Public: base64.StdEncoding.EncodeToString(pub[:]), FirstDevice: true}}
			}
			return netproto.MsgKeyResponse, resp, true
		case netproto.MsgChatSend:
			var msg netproto.ChatSend
			_ = netproto.Decode(f, &msg)
			sends = append(sends, msg)
			if len(sends) == 1 {
				phoneLinked = true
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

	if errText := sendChatWith(cm, "direct", "peer", "hi there", 0); errText != "" {
		t.Fatalf("send: %s", errText)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 2 {
		t.Fatalf("sends = %d", len(sends))
	}
	first, retry := sends[0], sends[1]
	if first.ClientMsgID == "" || retry.ClientMsgID != first.ClientMsgID {
		t.Fatalf("client ids %q / %q", first.ClientMsgID, retry.ClientMsgID)
	}
	if first.Text != "" || first.RecipientPublicKey != "" || len(first.Copies) != 2 || len(retry.Copies) != 3 {
		t.Fatalf("copies = %d then %d", len(first.Copies), len(retry.Copies))
	}
	ownPublic, ownPrivate, _ := cm.id.x25519()
	keys := map[string][32]byte{
		"peer-desktop": peerDesktop.xPrivate, "peer-phone": peerPhone.xPrivate, "self": ownPrivate,
	}
	for _, c := range retry.Copies {
		k, ok := keys[c.DeviceID]
		if !ok {
			t.Fatalf("copy for %q", c.DeviceID)
		}
		if got := openCopy(t, c.Text, ownPublic, k); got != "hi there" {
			t.Fatalf("%s opened %q", c.DeviceID, got)
		}
	}
}

func TestHistoryChunksStayWithinLimitsAndRoundTrip(t *testing.T) {
	recipientPub, recipientPriv, _ := box.GenerateKey(rand.Reader)
	senderPub, senderPriv, _ := box.GenerateKey(rand.Reader)
	body := strings.Repeat("x", 9000)
	messages := make([]netproto.HistoryMessage, 0, 200)
	for i := 200; i > 0; i-- { // newest first
		messages = append(messages, netproto.HistoryMessage{Peer: "peer", From: "peer", Body: body, SentAt: int64(i), ClientMsgID: "m"})
	}
	chunks, kept, err := buildHistoryChunks(messages, *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	if kept != len(messages) || len(chunks) < 2 {
		t.Fatalf("kept %d in %d chunks", kept, len(chunks))
	}
	var opened []netproto.HistoryMessage
	for _, data := range chunks {
		if len(data) > netproto.MaxHistoryChunk {
			t.Fatalf("chunk of %d bytes", len(data))
		}
		var nonce [24]byte
		copy(nonce[:], data[:24])
		plain, ok := box.Open(nil, data[24:], &nonce, senderPub, recipientPriv)
		if !ok {
			t.Fatal("chunk does not open with the device key")
		}
		var part []netproto.HistoryMessage
		if err := json.Unmarshal(plain, &part); err != nil {
			t.Fatal(err)
		}
		opened = append(opened, part...)
	}
	if len(opened) != len(messages) {
		t.Fatalf("opened %d messages", len(opened))
	}
	for i, m := range opened {
		if m.SentAt != int64(i+1) || m.Body != body {
			t.Fatalf("message %d is %d", i, m.SentAt)
		}
	}
	// The DeviceRequest carrying a chunk passes the protocol's own checks.
	request := netproto.DeviceRequest{Action: netproto.DeviceActionHistoryPut, DeviceID: "phone",
		History: &netproto.HistoryChunk{Seq: len(chunks) - 1, Total: len(chunks), Data: chunks[len(chunks)-1]}}
	if !request.Valid() {
		t.Fatal("history_put request is invalid")
	}
}

func TestHistoryChunksStopAtTheChunkLimit(t *testing.T) {
	recipientPub, _, _ := box.GenerateKey(rand.Reader)
	_, senderPriv, _ := box.GenerateKey(rand.Reader)
	// Each message fills most of a chunk, so the chunk count binds first.
	body := strings.Repeat("y", netproto.MaxHistoryChunk*2/3)
	messages := make([]netproto.HistoryMessage, netproto.MaxHistoryChunks+20)
	for i := range messages {
		messages[i] = netproto.HistoryMessage{Peer: "peer", From: "me", Self: true, Body: body, SentAt: int64(len(messages) - i)}
	}
	chunks, kept, err := buildHistoryChunks(messages, *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range chunks {
		total += len(c)
		if len(c) > netproto.MaxHistoryChunk {
			t.Fatalf("chunk of %d bytes", len(c))
		}
	}
	if len(chunks) > netproto.MaxHistoryChunks || total > netproto.MaxHistoryBytes || kept != len(chunks) || kept >= len(messages) {
		t.Fatalf("%d chunks, %d bytes, kept %d", len(chunks), total, kept)
	}
}
