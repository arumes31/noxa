package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"noxa/internal/netproto"
)

// presentedChat runs one live chat event through the decrypt layer and
// returns what the webview receives.
func presentedChat(t *testing.T, cm *connManager, chat netproto.ChatBroadcast) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"type": "chat", "data": chat})
	if err != nil {
		t.Fatal(err)
	}
	out := cm.maybeDecryptEvent(string(payload))
	if out == "" {
		t.Fatal("expected a synchronous result")
	}
	var event struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &event); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"enc", "e2e", "key_id"} {
		if _, present := event.Data[field]; present {
			t.Fatalf("crypto metadata %q reached the webview: %s", field, out)
		}
	}
	return event.Data
}

// TestLiveChatCarriesEncryptionMode pins the shield bug fix: a channel or
// global body this client opened is marked verified with its mode, so the
// renderer draws the shield; placeholders and plaintext never are.
func TestLiveChatCarriesEncryptionMode(t *testing.T) {
	cm := newConnManager(context.Background())
	channelKey, globalKey := [32]byte{3}, [32]byte{5}
	cm.scopeKeys.put(42, 7, channelKey)
	cm.scopeKeys.put(0, 9, globalKey)
	seal := func(text string, key [32]byte) string {
		blob, err := sealScope(text, key)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}

	for _, tc := range []struct {
		name     string
		chat     netproto.ChatBroadcast
		text     string
		mode     string
		direct   bool
		verified bool
	}{
		{name: "channel", chat: netproto.ChatBroadcast{ChannelID: "42", Text: seal("room", channelKey), Enc: true, KeyID: 7}, text: "room", mode: "server", verified: true},
		{name: "global", chat: netproto.ChatBroadcast{Text: seal("all", globalKey), Enc: true, KeyID: 9}, text: "all", mode: "server", verified: true},
		{name: "server encrypted direct is downgraded", chat: netproto.ChatBroadcast{Direct: true, ToUniqueID: "me", Text: seal("dm", globalKey), Enc: true, KeyID: 9}, text: "dm", mode: "downgraded", direct: true},
		{name: "tampered channel", chat: netproto.ChatBroadcast{ChannelID: "42", Text: seal("room", globalKey), Enc: true, KeyID: 7}, text: decryptFailedText},
		{name: "plaintext channel", chat: netproto.ChatBroadcast{ChannelID: "42", Text: "plain", EncVerified: true}, text: refusedPlainText},
		{name: "plaintext global claiming a mode", chat: netproto.ChatBroadcast{Text: "plain", EncMode: "e2e"}, text: refusedPlainText},
		{name: "plaintext direct", chat: netproto.ChatBroadcast{Direct: true, Text: "plain"}, text: refusedPlainText, direct: true},
		{name: "plaintext e2e claim", chat: netproto.ChatBroadcast{E2E: true, Text: "plain"}, text: refusedPlainText, direct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := presentedChat(t, cm, tc.chat)
			if data["text"] != tc.text {
				t.Fatalf("text = %v, want %q", data["text"], tc.text)
			}
			verified, _ := data["enc_verified"].(bool)
			mode, _ := data["enc_mode"].(string)
			direct, _ := data["direct"].(bool)
			if verified != tc.verified || mode != tc.mode || direct != tc.direct {
				t.Fatalf("verified/mode/direct = %v/%q/%v, want %v/%q/%v", verified, mode, direct, tc.verified, tc.mode, tc.direct)
			}
		})
	}

	cm.scopeKeys.markRefused(42, []uint32{8})
	data := presentedChat(t, cm, netproto.ChatBroadcast{ChannelID: "42", Text: "sealed", Enc: true, KeyID: 8})
	if data["text"] != refusedKeyText || data["enc_verified"] != nil || data["enc_mode"] != nil {
		t.Fatalf("refused generation presented as verified: %+v", data)
	}
}

// TestEndToEndDirectMessageCarriesLockMode verifies the E2E DM path marks its
// body "e2e" (the lock), distinct from a downgraded server-encrypted DM.
func TestEndToEndDirectMessageCarriesLockMode(t *testing.T) {
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
	message, err := alpha.encryptChat("direct", "bravo", "secret")
	if err != nil || message.KeyID != 0 || message.RecipientPublicKey == "" {
		t.Fatalf("e2e send = %+v, %v", message, err)
	}
	sink := make(dmPresentationSink, 1)
	bravo.sink = sink
	payload, err := json.Marshal(map[string]any{"type": "chat", "data": netproto.ChatBroadcast{
		Direct: true, FromUniqueID: "alpha", ToUniqueID: "bravo", Text: message.Text, Enc: true, E2E: true,
		SenderPublicKey: base64.StdEncoding.EncodeToString(alphaPub[:]), RecipientPublicKey: message.RecipientPublicKey, EncMode: "server",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out := bravo.maybeDecryptEvent(string(payload)); out != "" {
		t.Fatalf("expected asynchronous decrypt, got %s", out)
	}
	select {
	case out := <-sink:
		var event struct {
			Data netproto.ChatBroadcast `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &event); err != nil {
			t.Fatal(err)
		}
		if event.Data.Text != "secret" || !event.Data.EncVerified || event.Data.EncMode != "e2e" || !event.Data.Direct {
			t.Fatalf("e2e DM presentation = %+v", event.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("no decrypted event")
	}
}

func TestExportLinesNameTheEncryption(t *testing.T) {
	entry := netproto.ChatHistoryEntry{FromNickname: "ann", Body: "hi", SentAt: 1}
	for mode, tag := range map[string]string{"e2e": "[end-to-end encrypted] ann:", "server": "[server encrypted] ann:", "": "[unverified] ann:"} {
		if line := chatExportLine(entry, mode); !strings.Contains(line, tag) {
			t.Fatalf("mode %q line = %q", mode, line)
		}
	}
	if line := chatExportLine(netproto.ChatHistoryEntry{FromNickname: "ann", Deleted: true}, ""); strings.Contains(line, "[unverified]") {
		t.Fatalf("tombstone tagged: %q", line)
	}
	dm := dmExportMessages([]DMEntry{
		{FromNickname: "a", Body: "legacy verified", EncVerified: true},
		{FromNickname: "b", Body: "retired fallback", EncVerified: true, EncMode: "server"},
		{FromNickname: "c", Body: "legacy unverified"},
		{FromNickname: "d", Body: "unverified claim", EncMode: "e2e"},
		{FromNickname: "e", Body: "unknown mode", EncVerified: true, EncMode: "other"},
	}).Text
	for _, want := range []string{"[end-to-end encrypted] a:", "[unverified: server encrypted] b:", "[unverified] c:", "[unverified] d:", "[unverified] e:"} {
		if !strings.Contains(dm, want) {
			t.Fatalf("DM export %q lacks %q", dm, want)
		}
	}
}
