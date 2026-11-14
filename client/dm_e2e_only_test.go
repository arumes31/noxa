package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// TestDirectSendNeverFallsBackToServerKey drives encryptChat against a
// directory that answers in different ways. DMs are end-to-end only: a clean
// "no key" answer is a clear send error even with a global scope key in hand,
// and every other failure stays a retryable lookup error.
func TestDirectSendNeverFallsBackToServerKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  func() (netproto.MessageType, any)
		absent bool
	}{
		{name: "clean absent key", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}
		}, absent: true},
		{name: "mismatched peer", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "someone-else"}
		}},
		{name: "malformed key", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer", PublicKey: "not base64"}
		}},
		{name: "request error", reply: func() (netproto.MessageType, any) {
			return netproto.MsgError, netproto.Error{Code: 1, OriginType: uint16(netproto.MsgKeyRequest), Message: "busy"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chatSends int
			_, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				switch netproto.MessageType(f.Type) {
				case netproto.MsgKeyRequest:
					typ, body := tc.reply()
					return typ, body, true
				case netproto.MsgChatSend:
					chatSends++
				}
				return 0, nil, false
			})
			// A global scope key is available: it must still never be used.
			cm.scopeKeys.put(0, 5, [32]byte{9})
			msg, err := cm.encryptChat("direct", "peer", "hello")
			if err == nil {
				t.Fatalf("direct send without an end-to-end key produced %+v", msg)
			}
			if got := errors.Is(err, errPeerNoE2EKey); got != tc.absent {
				t.Fatalf("absent-key error = %v (%v), want %v", got, err, tc.absent)
			}
			if !tc.absent && !strings.Contains(err.Error(), "could not fetch the end-to-end key") {
				t.Fatalf("lookup failure error = %v", err)
			}
			// The send path shows the absent-key error as a complete sentence.
			result := sendChatWith(cm, "direct", "peer", "hello", 0)
			if tc.absent && (result != errPeerNoE2EKey.Error() || !strings.Contains(result, "need to update noxa")) {
				t.Fatalf("send result = %q", result)
			}
			if !tc.absent && !strings.HasPrefix(result, "encryption failed: ") {
				t.Fatalf("send result = %q", result)
			}
			if chatSends != 0 {
				t.Fatalf("%d chat sends reached the server", chatSends)
			}
		})
	}
}

// TestStaleKeyRetryNeverFallsBack: a stale-device rejection of an end-to-end
// DM followed by a directory answer of "no key" must fail the send rather
// than resend it server-readable.
func TestStaleKeyRetryNeverFallsBack(t *testing.T) {
	oldKey, _ := genPair(t)
	var sent []netproto.ChatSend
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(f.Type) == netproto.MsgKeyRequest {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}, true
		}
		var msg netproto.ChatSend
		if err := netproto.Decode(f, &msg); err != nil {
			t.Error(err)
		}
		sent = append(sent, msg)
		return netproto.MsgError, netproto.Error{Code: 7, OriginType: uint16(netproto.MsgChatSend), Message: "stale recipient", RecipientKeyStale: true}, true
	})
	cm.mu.Lock()
	cm.authorizationModel = netproto.AuthorizationModelRolesV1
	cm.mu.Unlock()
	cm.scopeKeys.put(0, 5, [32]byte{9})
	cacheTestPeer(t, cm, "peer", oldKey)
	result := app.SendChat("direct", "peer", "secret")
	if result != errPeerNoE2EKey.Error() || len(sent) != 1 {
		t.Fatalf("result=%q sends=%d", result, len(sent))
	}
	if sent[0].RecipientPublicKey != testPeerRecord(t, "peer", oldKey) || sent[0].KeyID != 0 {
		t.Fatal("first send was not end-to-end")
	}
}

// TestServerSealedDMIsDropped: this client never sends a DM sealed with the
// server-held key, so every such DM (from anyone, or an own echo) is dropped
// before it can reach a DM thread, whether or not its generation is known.
func TestServerSealedDMIsDropped(t *testing.T) {
	cm := newTestConnManager()
	cm.uniqueID = "me"
	globalKey := [32]byte{5}
	cm.scopeKeys.put(0, 9, globalKey)
	blob, err := sealScope("dm", globalKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []netproto.ChatBroadcast{
		{Direct: true, FromUniqueID: "stranger", ToUniqueID: "me", Text: blob, Enc: true, KeyID: 9},
		{Direct: true, FromUniqueID: "friend", ToUniqueID: "me", Text: blob, Enc: true, KeyID: 9, EncVerified: true, EncMode: "server"},
		{Direct: true, FromUniqueID: "me", ToUniqueID: "friend", Text: blob, Enc: true, KeyID: 9},
		{Direct: true, FromUniqueID: "friend", ToUniqueID: "me", Text: blob, Enc: true, KeyID: 11},
	} {
		payload, err := json.Marshal(map[string]any{"type": "chat", "data": chat})
		if err != nil {
			t.Fatal(err)
		}
		if out := cm.maybeDecryptEvent(string(payload)); out != "" {
			t.Fatalf("server-sealed DM %s→%s reached the UI: %s", chat.FromUniqueID, chat.ToUniqueID, out)
		}
	}
}

// TestDMEncStateOnlyVerifiesEndToEnd: storage, search and export verify
// only end-to-end DMs. A verified "server" record (the retired fallback) and
// any "downgraded" record read back unverified under the downgraded warning.
func TestDMEncStateOnlyVerifiesEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		entry    DMEntry
		verified bool
		mode     string
	}{
		{DMEntry{EncVerified: true}, true, "e2e"},
		{DMEntry{EncVerified: true, EncMode: "e2e"}, true, "e2e"},
		{DMEntry{EncVerified: true, EncMode: "server"}, false, "downgraded"},
		{DMEntry{EncVerified: true, EncMode: "downgraded"}, false, "downgraded"},
		{DMEntry{EncMode: "downgraded"}, false, "downgraded"},
		{DMEntry{EncMode: "server"}, false, ""},
		{DMEntry{EncVerified: true, EncMode: "bogus"}, false, ""},
		{DMEntry{EncMode: "e2e"}, false, ""},
		{DMEntry{}, false, ""},
	} {
		verified, mode := dmEncState(tc.entry)
		if verified != tc.verified || mode != tc.mode || dmEncMode(tc.entry) != tc.mode {
			t.Fatalf("dmEncState(%+v) = %v/%q, want %v/%q", tc.entry, verified, mode, tc.verified, tc.mode)
		}
	}
	for _, tc := range []struct {
		in       DMEntry
		verified bool
		mode     string
	}{
		{DMEntry{EncVerified: true, EncMode: "e2e"}, true, "e2e"},
		{DMEntry{EncVerified: true, EncMode: "server"}, false, "downgraded"},
		{DMEntry{EncVerified: true, EncMode: "downgraded"}, false, "downgraded"},
		{DMEntry{EncMode: "downgraded"}, false, "downgraded"},
		// A new verified record must name its mode.
		{DMEntry{EncVerified: true}, false, ""},
		{DMEntry{EncVerified: true, EncMode: "bogus"}, false, ""},
		{DMEntry{EncMode: "e2e"}, false, ""},
	} {
		e := tc.in
		normalizeDMEncryption(&e)
		if e.EncVerified != tc.verified || e.EncMode != tc.mode {
			t.Fatalf("normalize(%+v) = %+v, want %v/%q", tc.in, e, tc.verified, tc.mode)
		}
	}
	line := chatExportLine(netproto.ChatHistoryEntry{FromNickname: "Mia", Body: "hi", SentAt: 1}, dmEncMode(DMEntry{EncMode: "downgraded"}))
	if !strings.Contains(line, "[unverified: server encrypted] Mia:") {
		t.Fatalf("export line = %q", line)
	}
	out := dmExportMessages([]DMEntry{{FromNickname: "Mia", Body: "hi", SentAt: 1, EncVerified: true, EncMode: "server"}})
	if !strings.Contains(out.Text, "[unverified: server encrypted] Mia:") {
		t.Fatalf("export = %q", out.Text)
	}
}

// TestServerDMRecordRoundTripsDowngraded: a DM appended as verified
// "server" (or "downgraded") reloads, searches and exports as unverified
// "downgraded", never as a verified shield.
func TestServerDMRecordRoundTripsDowngraded(t *testing.T) {
	_, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return 0, nil, false
	})
	a := newLocalApp(t, cm)
	for i, mode := range []string{"server", "downgraded"} {
		e := DMEntry{FromUniqueID: "friend", FromNickname: "mia", Body: "needle " + mode, SentAt: int64(5 + i), EncVerified: true, EncMode: mode}
		if err := a.DMHistoryAppend("friend", "mia", e); err != "" {
			t.Fatalf("append: %s", err)
		}
	}
	msgs, err := a.DMHistoryLoad("friend")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("reloaded = %+v, %v", msgs, err)
	}
	for _, m := range msgs {
		if m.EncVerified || m.EncMode != "downgraded" {
			t.Fatalf("reloaded record = %+v", m)
		}
	}
	res, err := a.DMSearch("", "needle", 0)
	if err != nil || len(res.Messages) != 2 {
		t.Fatalf("search = %+v, %v", res.Messages, err)
	}
	for _, m := range res.Messages {
		if m.EncVerified || m.EncMode != "downgraded" {
			t.Fatalf("search hit = %+v", m)
		}
	}
	export, err := a.DMExportHistory("friend")
	if err != nil || strings.Contains(export.Text, "[server encrypted]") || strings.Count(export.Text, "[unverified: server encrypted]") != 2 {
		t.Fatalf("export = %q, %v", export.Text, err)
	}
}
