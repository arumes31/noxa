package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"noxa/internal/netproto"
)

func TestPrivateCallUsesPinnedParticipantDeviceKeys(t *testing.T) {
	for _, mode := range []string{"bound", "legacy", "missing peer", "malformed peer", "wrong own", "partial without flag", "wrong own session", "ended", "peer not accepted"} {
		t.Run(mode, func(t *testing.T) {
			peerPub, peerPriv := genPair(t)
			wrongPub, _ := genPair(t)
			var cm *connManager
			signals := make(chan string, 2)
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var request netproto.CallRequest
				if err := netproto.Decode(frame, &request); err != nil {
					t.Error(err)
				}
				if request.Action == "signal" {
					signals <- request.Signal
				}
				ownPub, _, err := cm.id.x25519()
				if err != nil {
					t.Error(err)
				}
				own := map[string]any{"unique_id": "me", "client_id": "mine", "state": "accepted", "public_key": base64.StdEncoding.EncodeToString(ownPub[:])}
				peer := map[string]any{"unique_id": "peer", "client_id": "peer-device", "state": "accepted", "public_key": base64.StdEncoding.EncodeToString(peerPub[:])}
				call := map[string]any{"id": "call", "caller": "me", "revision": 2, "key_binding": true, "participants": []any{own, peer}}
				switch mode {
				case "legacy":
					delete(call, "key_binding")
					delete(own, "public_key")
					delete(peer, "public_key")
				case "missing peer":
					delete(peer, "public_key")
				case "malformed peer":
					peer["public_key"] = "invalid"
				case "wrong own":
					own["public_key"] = base64.StdEncoding.EncodeToString(wrongPub[:])
				case "partial without flag":
					delete(call, "key_binding")
					delete(peer, "public_key")
				case "wrong own session":
					own["client_id"] = "another-device"
				case "ended":
					call["ended_at"] = 1
				case "peer not accepted":
					peer["state"] = "left"
				}
				return netproto.MsgCallResult, map[string]any{"action": request.Action, "call": call}, true
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}}
			app.activeID = "a"
			cm.mu.Lock()
			cm.uniqueID, cm.clientID = "me", "mine"
			cm.mu.Unlock()
			cm.pubKeys.put("peer", peerPub)
			if mode == "bound" {
				cm.pubKeys.put("peer", wrongPub)
			}
			valid := mode == "bound" || mode == "legacy"
			err := app.SendPrivateCallDescriptionForTab("a", "call", "peer", "offer", "v=0\r\na=fingerprint:sha-256 01:02\r\n")
			if (err == nil) != valid {
				t.Fatalf("send valid=%v err=%v", valid, err)
			}
			ownPub, _, err := cm.id.x25519()
			if err != nil {
				t.Fatal(err)
			}
			if valid {
				if _, err := openDM(<-signals, ownPub, peerPriv); err != nil {
					t.Fatalf("pinned peer device cannot decrypt: %v", err)
				}
			} else if len(signals) != 0 {
				t.Fatal("invalid binding relayed a description")
			}
			plain, err := json.Marshal(PrivateCallDescription{CallID: "call", From: "peer", To: "me", Type: "answer", SDP: "v=0\r\na=fingerprint:sha-256 01:02\r\n"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := sealDM(string(plain), ownPub, peerPriv)
			if err != nil {
				t.Fatal(err)
			}
			description, err := app.OpenPrivateCallDescriptionForTab("a", netproto.CallSignal{CallID: "call", From: "peer", To: "me", Body: body})
			if (err == nil) != valid || (valid && description.Type != "answer") {
				t.Fatalf("open valid=%v description=%+v err=%v", valid, description, err)
			}
		})
	}
}
