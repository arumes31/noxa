package main

import (
	"encoding/json"
	"testing"
	"time"
)

type dmPresentationSink chan string

func (sink dmPresentationSink) Emit(name string, value any) {
	if name == "event" {
		sink <- value.(string)
	}
}

func TestDecryptedDMRetainsScopeAndUsesRecipientForOwnEcho(t *testing.T) {
	alpha, bravo := newTestConnManager(), newTestConnManager()
	alpha.id, bravo.id = mustTempIdentity(t), mustTempIdentity(t)
	alpha.uniqueID, bravo.uniqueID = "alpha", "bravo"
	alphaPub, _, err := alpha.id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	bravoPub, _, err := bravo.id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	alpha.pubKeys.put("bravo", bravoPub)
	bravo.pubKeys.put("alpha", alphaPub)
	message, err := alpha.encryptChat("direct", "bravo", "private Grüße 🌿")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		manager   *connManager
		cipher    string
		recipient string
		verified  bool
	}{
		{name: "receiver", manager: bravo, cipher: message.Text, recipient: "bravo", verified: true},
		{name: "sender echo", manager: alpha, cipher: message.Text, recipient: "bravo", verified: true},
		{name: "legacy incoming scope", manager: bravo, cipher: message.Text, verified: true},
		{name: "legacy echo cannot verify without recipient", manager: alpha, cipher: message.Text},
		{name: "corrupt incoming remains private", manager: bravo, cipher: "corrupt", recipient: "bravo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := make(dmPresentationSink, 1)
			tc.manager.sink = sink
			payload, err := json.Marshal(map[string]any{"type": "chat", "data": map[string]any{
				"from_unique_id": "alpha", "to_unique_id": tc.recipient,
				"text": tc.cipher, "enc": true, "e2e": true, "enc_verified": true, "client_msg_id": message.ClientMsgID,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if result := tc.manager.maybeDecryptEvent(string(payload)); result != "" {
				t.Fatalf("expected asynchronous decryption, got %s", result)
			}
			var result string
			select {
			case result = <-sink:
			case <-time.After(time.Second):
				t.Fatal("no decrypted event emitted")
			}
			var event struct {
				Data struct {
					Direct    bool   `json:"direct"`
					Verified  bool   `json:"enc_verified"`
					Text      string `json:"text"`
					Recipient string `json:"to_unique_id"`
					Enc       bool   `json:"enc"`
					E2E       bool   `json:"e2e"`
					KeyID     uint32 `json:"key_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(result), &event); err != nil {
				t.Fatal(err)
			}
			if !event.Data.Direct || event.Data.Recipient != tc.recipient || event.Data.Verified != tc.verified {
				t.Fatalf("lost scope/recipient or false verification: %s", result)
			}
			want := missingKeyText
			if tc.verified {
				want = "private Grüße 🌿"
			}
			if event.Data.Text != want || event.Data.Enc || event.Data.E2E || event.Data.KeyID != 0 {
				t.Fatalf("wrong plaintext presentation or retained crypto metadata: %s", result)
			}
		})
	}
}

func TestWirePlaintextCannotClaimLocalEncryptionVerification(t *testing.T) {
	cm := newTestConnManager()
	result := cm.maybeDecryptEvent(`{"type":"chat","data":{"e2e":true,"text":"unverified","enc_verified":true}}`)
	var event struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(result), &event); err != nil {
		t.Fatal(err)
	}
	if event.Data["enc_verified"] != nil || event.Data["e2e"] != nil || event.Data["direct"] != true || event.Data["text"] != "unverified" {
		t.Fatalf("wire plaintext retained a false verification claim: %s", result)
	}
}
