package main

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

const testGroupID = "0b6f4b8e-3f0c-4a3e-9d55-2f1f4c6a7b10"

func TestConversationDeviceEnvelopesReachPhonesAndOwnDevices(t *testing.T) {
	cm, pub, priv := deviceCopyManager(t)
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
	ownPhone := newTestDevice(t, "own-phone", "web")
	pendingPhone := newTestDevice(t, "pending-phone", "web")
	cm.dmDevices.put("peer", []dmTrustedDevice{{DeviceID: peerDesktop.id, X25519: peerDesktop.xPublic, First: true}, {DeviceID: peerPhone.id, X25519: peerPhone.xPublic}}, 0, time.Now())
	// This desktop is not the account's first device here: it is still
	// skipped, since it holds the plaintext already.
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "first", X25519: pub, First: true}, {DeviceID: "self", X25519: pub}, {DeviceID: ownPhone.id, X25519: ownPhone.xPublic}}, 0, time.Now())
	cm.dmDevices.put("invitee", []dmTrustedDevice{{DeviceID: pendingPhone.id, X25519: pendingPhone.xPublic}}, 0, time.Now())
	group := netproto.Conversation{ID: testGroupID, Epoch: 3, Members: []netproto.ConversationMember{
		{UniqueID: "me"}, {UniqueID: "peer"}, {UniqueID: "invitee", Pending: true},
	}}
	plaintexts := map[string]string{"me": `{"to":"me"}`, "peer": `{"to":"peer"}`}
	envelopes, err := cm.conversationDeviceEnvelopes(group, plaintexts, priv, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		key   [32]byte
		plain string
	}{
		netproto.DeviceEnvelopeKey("peer", peerPhone.id): {peerPhone.xPrivate, plaintexts["peer"]},
		netproto.DeviceEnvelopeKey("me", ownPhone.id):    {ownPhone.xPrivate, plaintexts["me"]},
	}
	if len(envelopes) != len(want) {
		t.Fatalf("envelopes = %v", envelopes)
	}
	for key, w := range want {
		blob, ok := envelopes[key]
		if !ok {
			t.Fatalf("missing envelope %s", key)
		}
		if got := openCopy(t, blob, pub, w.key); got != w.plain {
			t.Fatalf("%s opened %q", key, got)
		}
	}
}

func TestConversationSendRetriesStaleDevicesWithTheSameReference(t *testing.T) {
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
	group := netproto.Conversation{ID: testGroupID, Name: "Group", Owner: "me", Revision: 1, Epoch: 1,
		Members: []netproto.ConversationMember{{UniqueID: "me", JoinedEpoch: 1}, {UniqueID: "peer", JoinedEpoch: 1}}}
	var mu sync.Mutex
	var sends []netproto.ConversationSend
	var cm *connManager
	directory := testPeerDirectory(t, func() *connManager { return cm }, peerDesktop, peerPhone)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		mu.Lock()
		defer mu.Unlock()
		if netproto.MessageType(f.Type) == netproto.MsgKeyRequest {
			var r netproto.KeyRequest
			_ = netproto.Decode(f, &r)
			return netproto.MsgKeyResponse, directory(r), true
		}
		var request netproto.ConversationRequest
		_ = netproto.Decode(f, &request)
		result := netproto.ConversationResult{Action: request.Action}
		switch request.Action {
		case "get":
			result.Conversations = []netproto.Conversation{group}
		case "send":
			sends = append(sends, *request.Message)
			if len(sends) == 1 {
				return netproto.MsgError, netproto.Error{Code: 409, OriginType: uint16(netproto.MsgConversationRequest), RecipientDevicesStale: true, Message: "stale"}, true
			}
			result.MessageID = 7
		}
		return netproto.MsgConversationResult, result, true
	})
	cm.mu.Lock()
	cm.uniqueID, cm.deviceID, cm.supportsDMDevices = "me", "self", true
	cm.mu.Unlock()
	// A cached list that misses the peer's phone makes the first send stale.
	cm.dmDevices.put("peer", []dmTrustedDevice{{DeviceID: peerDesktop.id, X25519: peerDesktop.xPublic, First: true}}, 0, time.Now())
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	id, err := app.SendConversationForTab("a", group.ID, "hello group", "ref-1")
	if err != nil || id.MessageID != 7 || id.Protocol != "legacy" || id.Transport != "" {
		t.Fatalf("send = %+v, %v", id, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sends) != 2 || sends[0].Reference != "ref-1" || sends[1].Reference != "ref-1" {
		t.Fatalf("sends = %+v", sends)
	}
	if len(sends[0].DeviceEnvelopes) != 0 || len(sends[1].DeviceEnvelopes) != 1 {
		t.Fatalf("device envelopes %d then %d", len(sends[0].DeviceEnvelopes), len(sends[1].DeviceEnvelopes))
	}
	ownPublic, _, _ := cm.id.x25519()
	plain := openCopy(t, sends[1].DeviceEnvelopes[netproto.DeviceEnvelopeKey("peer", peerPhone.id)], ownPublic, peerPhone.xPrivate)
	var envelope conversationPlaintext
	if err := json.Unmarshal([]byte(plain), &envelope); err != nil || envelope.To != "peer" || envelope.Text != "hello group" || envelope.Reference != "ref-1" {
		t.Fatalf("phone envelope = %q", plain)
	}
}

func TestConversationHistoryOpensPhoneMessagesOnlyFromTrustedDevices(t *testing.T) {
	peerDesktop := newTestDevice(t, "peer-desktop", "desktop")
	peerPhone := newTestDevice(t, "peer-phone", "web")
	stranger := newTestDevice(t, "stranger", "web")
	var cm *connManager
	directory := testPeerDirectory(t, func() *connManager { return cm }, peerDesktop, peerPhone)
	var messages []netproto.ConversationMessage
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(f.Type) == netproto.MsgKeyRequest {
			var r netproto.KeyRequest
			_ = netproto.Decode(f, &r)
			return netproto.MsgKeyResponse, directory(r), true
		}
		var request netproto.ConversationRequest
		_ = netproto.Decode(f, &request)
		return netproto.MsgConversationResult, netproto.ConversationResult{Action: request.Action, Messages: messages}, true
	})
	cm.mu.Lock()
	cm.uniqueID, cm.deviceID, cm.supportsDMDevices = "me", "self", true
	cm.mu.Unlock()
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	ownPublic, _, _ := cm.id.x25519()
	sealedBy := func(id int64, device *testDevice, text string) netproto.ConversationMessage {
		ref := "ref-" + device.id
		plain, _ := json.Marshal(conversationPlaintext{ConversationID: testGroupID, Epoch: 1, Reference: ref, From: "peer", To: "me", Text: text})
		body, err := sealDM(string(plain), ownPublic, device.xPrivate)
		if err != nil {
			t.Fatal(err)
		}
		return netproto.ConversationMessage{ID: id, ConversationID: testGroupID, Epoch: 1, FromUniqueID: "peer", Reference: ref, Body: body,
			SenderDeviceID: device.id, SenderDeviceKey: base64.StdEncoding.EncodeToString(device.xPublic[:]), EncVerified: true}
	}
	trusted := sealedBy(2, peerPhone, "from the phone")
	untrusted := sealedBy(1, stranger, "from a stranger")
	// A trusted device id paired with a key the directory does not list.
	swapped := sealedBy(3, stranger, "swapped key")
	swapped.SenderDeviceID = peerPhone.id
	messages = []netproto.ConversationMessage{swapped, trusted, untrusted}

	result, err := app.ConversationForTab("a", netproto.ConversationRequest{Action: "history", ID: testGroupID})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]netproto.ConversationMessage{}
	for _, m := range result.Messages {
		got[m.ID] = m
	}
	if m := got[2]; !m.EncVerified || m.Body != "from the phone" {
		t.Fatalf("trusted phone message = %+v", m)
	}
	for _, id := range []int64{1, 3} {
		if m := got[id]; m.EncVerified || m.Body == "from a stranger" || m.Body == "swapped key" {
			t.Fatalf("untrusted message %d opened: %+v", id, m)
		}
	}
}
