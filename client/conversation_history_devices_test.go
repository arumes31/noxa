package main

import (
	"encoding/base64"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestHistoricalConversationSenderDoesNotReactivateDevice(t *testing.T) {
	_, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("own history must not query another identity")
		return netproto.MsgError, nil, true
	})
	cm.uniqueID = "alice"
	id, err := cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	first := netproto.DMDevice{DeviceID: "desktop", FirstDevice: true, Ed25519Public: id.PublicKey, X25519Public: base64.StdEncoding.EncodeToString(pub[:])}
	phone := newTestDevice(t, "phone", "web")
	entry := phone.deviceInfo
	entry.ApprovedBy = first.DeviceID
	entry.ApprovalSignature, err = auth.SignChallenge(id.PrivateKey, netproto.DeviceApprovalTranscript("alice", first.DeviceID, entry.Ed25519Public, entry.X25519Public))
	if err != nil {
		t.Fatal(err)
	}
	cm.dmDevices.put("alice", []dmTrustedDevice{{DeviceID: "desktop", X25519: pub, First: true}}, 0, time.Now())
	message := netproto.ConversationMessage{FromUniqueID: "alice", SenderDeviceID: "phone", SenderDeviceKey: entry.X25519Public, SenderDeviceChain: []netproto.DMDevice{entry, first}}
	if !cm.trustedHistoricalConversationSender(message, phone.xPublic) {
		t.Fatal("valid historical proof rejected")
	}
	live, _, ok := cm.dmDevices.get("alice", time.Now())
	if !ok || len(live) != 1 || live[0].DeviceID != "desktop" {
		t.Fatalf("historical proof changed live recipients: %+v", live)
	}
	message.SenderDeviceChain[0].ApprovalSignature = make([]byte, 64)
	if cm.trustedHistoricalConversationSender(message, phone.xPublic) {
		t.Fatal("forged historical proof accepted")
	}
}
