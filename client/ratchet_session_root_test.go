package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

func TestRatchetDeviceKeyRejectsInvalidRootWithDiagnostic(t *testing.T) {
	peer := newTestDevice(t, "peer-desktop", "desktop")
	first := peer.deviceInfo
	first.FirstDevice = true
	signing, err := auth.LoadPrivateKey(peer.edPrivate)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := e2ee.SignKeyRecord("peer", peer.xPublic, signing)
	if err != nil {
		t.Fatal(err)
	}
	wrongUID, err := e2ee.SignKeyRecord("other", peer.xPublic, signing)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, root string }{
		{"unsigned", base64.StdEncoding.EncodeToString(peer.xPublic[:])},
		{"malformed", "noxa-dm-key-v1:invalid"},
		{"wrong identity", wrongUID},
	} {
		for _, pinned := range []bool{false, true} {
			name := test.name + "/first contact"
			if pinned {
				name = test.name + "/pinned contact"
			}
			t.Run(name, func(t *testing.T) {
				_, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
					if netproto.MessageType(f.Type) != netproto.MsgKeyRequest {
						return 0, nil, false
					}
					return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer", PublicKey: test.root, Devices: []netproto.DMDevice{first}}, true
				})
				cm.mu.Lock()
				cm.uniqueID = "self"
				cm.mu.Unlock()
				path, _, err := cm.peerPinPath("peer")
				if err != nil {
					t.Fatal(err)
				}
				var before []byte
				if pinned {
					if _, err := cm.verifyPeerRecord("peer", valid, false); err != nil {
						t.Fatal(err)
					}
					before, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				key, err := cm.ratchetDeviceKey(context.Background(), "peer", peer.id)
				if key != nil || ratchetCode(err) != "untrusted_device" || !errors.Is(err, e2ee.ErrKeyRecord) {
					t.Fatalf("invalid root result: key present=%t code=%q error=%v", key != nil, ratchetCode(err), err)
				}
				after, readErr := os.ReadFile(path)
				if pinned {
					if readErr != nil || !bytes.Equal(before, after) {
						t.Fatal("invalid root modified the existing contact pin")
					}
				} else if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("invalid root created a contact pin")
				}
			})
		}
	}
}
