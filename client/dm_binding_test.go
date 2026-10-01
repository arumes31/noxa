package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestDirectMessageRetriesOnlyRejectedStaleDeviceKey(t *testing.T) {
	for _, outcome := range []string{"accepted refresh", "repeated stale", "other rejection", "wrong origin"} {
		t.Run(outcome, func(t *testing.T) {
			oldKey, _ := genPair(t)
			newKey, newPrivate := genPair(t)
			var sent []netproto.ChatSend
			lookups := 0
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				if netproto.MessageType(f.Type) == netproto.MsgKeyRequest {
					lookups++
					return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer", PublicKey: base64.StdEncoding.EncodeToString(newKey[:])}, true
				}
				var msg netproto.ChatSend
				if err := netproto.Decode(f, &msg); err != nil {
					t.Error(err)
				}
				sent = append(sent, msg)
				if len(sent) == 1 || outcome == "repeated stale" {
					failure := netproto.Error{Code: 7, OriginType: uint16(netproto.MsgChatSend), Message: "stale recipient", RecipientKeyStale: true}
					if outcome == "other rejection" {
						failure.RecipientKeyStale = false
					}
					if outcome == "wrong origin" {
						failure.OriginType = uint16(netproto.MsgChatEdit)
					}
					return netproto.MsgError, failure, true
				}
				return netproto.MsgChatAccepted, netproto.ChatAccepted{ClientMsgID: msg.ClientMsgID, ToUniqueID: msg.ToUniqueID, Disposition: netproto.ChatRelayed}, true
			})
			cm.mu.Lock()
			cm.authorizationModel = netproto.AuthorizationModelRolesV1
			cm.mu.Unlock()
			cm.pubKeys.put("peer", oldKey)
			result := app.SendChat("direct", "peer", "freshly sealed retry")
			wantRetry := outcome == "accepted refresh" || outcome == "repeated stale"
			wantSends := 1
			if wantRetry {
				wantSends = 2
			}
			if len(sent) != wantSends || lookups != wantSends-1 {
				t.Fatalf("sends=%d lookups=%d result=%q", len(sent), lookups, result)
			}
			if (result == "") != (outcome == "accepted refresh") {
				t.Fatalf("unexpected result %q", result)
			}
			if !wantRetry {
				return
			}
			if sent[0].ClientMsgID != sent[1].ClientMsgID || sent[0].Text == sent[1].Text {
				t.Fatal("retry must preserve reference and reseal ciphertext")
			}
			if sent[1].RecipientPublicKey != base64.StdEncoding.EncodeToString(newKey[:]) {
				t.Fatal("retry retained stale device binding")
			}
			pub, _, err := cm.id.x25519()
			if err != nil {
				t.Fatal(err)
			}
			plain, err := openDM(sent[1].Text, pub, newPrivate)
			if err != nil || !strings.Contains(plain, "freshly sealed retry") {
				t.Fatalf("fresh recipient cannot decrypt retry: %q %v", plain, err)
			}
		})
	}
}

func TestChatSendRejectedAfterDisconnect(t *testing.T) {
	for _, msg := range []netproto.ChatSend{
		{ToUniqueID: "peer", Text: "sealed direct message"},
		{ChannelID: "1", Text: "sealed channel message"},
	} {
		manager := &connManager{}
		if err := manager.sendChatWithKeyRetry(msg, "plaintext"); err == nil || !strings.Contains(err.Error(), "not connected") {
			t.Fatalf("disconnected send should return not connected, got %v", err)
		}
	}
}
