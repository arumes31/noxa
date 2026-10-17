package main

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestDMServerFallbackDecision(t *testing.T) {
	for _, tc := range []struct {
		lookup   peerKeyLookup
		knownE2E bool
		want     bool
	}{
		{peerKeyAbsent, false, true},
		{peerKeyAbsent, true, false},
		{peerKeyFailed, false, false},
		{peerKeyFailed, true, false},
		{peerKeyFound, false, false},
	} {
		if got := dmServerFallbackAllowed(tc.lookup, tc.knownE2E); got != tc.want {
			t.Fatalf("fallback(%d, known=%v) = %v, want %v", tc.lookup, tc.knownE2E, got, tc.want)
		}
	}
}

// TestDirectSendFallsBackOnlyOnCleanAbsentKey drives encryptChat against a
// directory that answers in different ways: only a clean "no key" answer for
// a peer without E2E history may use the server-encrypted path.
func TestDirectSendFallsBackOnlyOnCleanAbsentKey(t *testing.T) {
	globalKey := [32]byte{9}
	for _, tc := range []struct {
		name     string
		reply    func() (netproto.MessageType, any)
		known    bool
		history  bool
		noScope  bool
		fallback bool
	}{
		{name: "clean absent key without a server key", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}
		}, noScope: true},
		{name: "clean absent key", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}
		}, fallback: true},
		{name: "absent key for a known e2e peer", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}
		}, known: true},
		{name: "absent key with stored e2e history", reply: func() (netproto.MessageType, any) {
			return netproto.MsgKeyResponse, netproto.KeyResponse{UniqueID: "peer"}
		}, history: true},
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
			_, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				if netproto.MessageType(f.Type) != netproto.MsgKeyRequest {
					return 0, nil, false
				}
				typ, body := tc.reply()
				return typ, body, true
			})
			if !tc.noScope {
				cm.scopeKeys.put(0, 5, globalKey)
			}
			if tc.known {
				cm.e2ePeers.record("peer")
			}
			if tc.history {
				a := newLocalApp(t, cm)
				if err := a.DMHistoryAppend("peer", "Peer", DMEntry{FromUniqueID: "peer", Body: "earlier", EncVerified: true, EncMode: "e2e"}); err != "" {
					t.Fatal(err)
				}
				cm.dmHistory = a.dmHistoryForIdentity
			}
			msg, err := cm.encryptChat("direct", "peer", "hello")
			if !tc.fallback {
				if err == nil {
					t.Fatalf("downgraded to server encryption: %+v", msg)
				}
				if tc.noScope && !strings.Contains(err.Error(), "plaintext messages are not allowed") {
					t.Fatalf("keyless direct send error = %v", err)
				}
				return
			}
			if err != nil || msg.KeyID != 5 || msg.RecipientPublicKey != "" {
				t.Fatalf("fallback = %+v, %v", msg, err)
			}
			if text, err := openScope(msg.Text, globalKey); err != nil || text != "hello" {
				t.Fatalf("open fallback = %q, %v", text, err)
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
	cm.pubKeys.put("peer", oldKey)
	result := app.SendChat("direct", "peer", "secret")
	if result == "" || len(sent) != 1 || !strings.Contains(result, "downgrade") {
		t.Fatalf("result=%q sends=%d", result, len(sent))
	}
	if sent[0].RecipientPublicKey != base64.StdEncoding.EncodeToString(oldKey[:]) {
		t.Fatal("first send was not end-to-end")
	}
}

// TestServerDMFromKnownE2EPeerIsNotShielded: a server-encrypted DM from a
// peer with end-to-end history loses the verified mark (warning badge); one
// from a peer without it keeps the shield.
func TestServerDMFromKnownE2EPeerIsNotShielded(t *testing.T) {
	cm := newTestConnManager()
	cm.uniqueID = "me"
	globalKey := [32]byte{5}
	cm.scopeKeys.put(0, 9, globalKey)
	blob, err := sealScope("dm", globalKey)
	if err != nil {
		t.Fatal(err)
	}
	chat := func(from string) netproto.ChatBroadcast {
		return netproto.ChatBroadcast{Direct: true, FromUniqueID: from, ToUniqueID: "me", Text: blob, Enc: true, KeyID: 9}
	}
	if data := presentedChat(t, cm, chat("stranger")); data["enc_verified"] != true || data["enc_mode"] != "server" {
		t.Fatalf("server DM from a peer without e2e history = %+v", data)
	}
	cm.e2ePeers.record("friend")
	data := presentedChat(t, cm, chat("friend"))
	// The body was opened with the server key: it is shown, unverified, under
	// the "downgraded" mode rather than refused as plaintext.
	if data["enc_verified"] != nil || data["enc_mode"] != "downgraded" || data["direct"] != true || data["text"] != "dm" {
		t.Fatalf("downgraded DM = %+v", data)
	}
}

// TestFlagDowngradedDMKeepsPlaceholdersModeless: an unopened body is never
// labelled downgraded, and the flag never verifies anything.
func TestFlagDowngradedDMKeepsPlaceholdersModeless(t *testing.T) {
	cm := newTestConnManager()
	cm.uniqueID = "me"
	cm.e2ePeers.record("friend")
	unopened := netproto.ChatBroadcast{Direct: true, FromUniqueID: "friend", ToUniqueID: "me", Text: missingKeyText}
	cm.flagDowngradedDM(&unopened)
	if unopened.EncVerified || unopened.EncMode != "" {
		t.Fatalf("placeholder flagged: %+v", unopened)
	}
	own := netproto.ChatBroadcast{Direct: true, FromUniqueID: "me", ToUniqueID: "friend", Text: "hi", EncVerified: true, EncMode: "server"}
	cm.flagDowngradedDM(&own)
	if own.EncVerified || own.EncMode != "downgraded" {
		t.Fatalf("own downgraded echo = %+v", own)
	}
	stranger := netproto.ChatBroadcast{Direct: true, FromUniqueID: "stranger", ToUniqueID: "me", Text: "hi", EncVerified: true, EncMode: "server"}
	cm.flagDowngradedDM(&stranger)
	if !stranger.EncVerified || stranger.EncMode != "server" {
		t.Fatalf("stranger lost the shield: %+v", stranger)
	}
}

// TestDowngradedDMRecordStaysUnverified: storage, search and export keep a
// downgraded record unverified and named, and never count it as e2e history.
func TestDowngradedDMRecordStaysUnverified(t *testing.T) {
	for _, e := range []DMEntry{{EncMode: "downgraded"}, {EncVerified: true, EncMode: "downgraded"}} {
		normalizeDMEncryption(&e)
		if e.EncVerified || e.EncMode != "downgraded" {
			t.Fatalf("stored downgraded record = %+v", e)
		}
		if got := dmEncMode(e); got != "downgraded" {
			t.Fatalf("dmEncMode = %q", got)
		}
	}
	line := chatExportLine(netproto.ChatHistoryEntry{FromNickname: "Mia", Body: "hi", SentAt: 1}, dmEncMode(DMEntry{EncMode: "downgraded"}))
	if !strings.Contains(line, "[unverified: server encrypted") || strings.Contains(line, "[server encrypted]") {
		t.Fatalf("export line = %q", line)
	}
	out := dmExportMessages([]DMEntry{{FromNickname: "Mia", Body: "hi", SentAt: 1, EncMode: "downgraded"}})
	if !strings.Contains(out.Text, "[unverified: server encrypted") {
		t.Fatalf("export = %q", out.Text)
	}
}

func TestDMEncModeNeedsVerification(t *testing.T) {
	for _, tc := range []struct {
		entry DMEntry
		want  string
	}{
		{DMEntry{EncVerified: true}, "e2e"},
		{DMEntry{EncVerified: true, EncMode: "e2e"}, "e2e"},
		{DMEntry{EncVerified: true, EncMode: "server"}, "server"},
		{DMEntry{EncVerified: true, EncMode: "bogus"}, ""},
		{DMEntry{EncMode: "e2e"}, ""},
		{DMEntry{}, ""},
	} {
		if got := dmEncMode(tc.entry); got != tc.want {
			t.Fatalf("dmEncMode(%+v) = %q, want %q", tc.entry, got, tc.want)
		}
	}
	for _, e := range []DMEntry{{EncVerified: true}, {EncVerified: true, EncMode: "bogus"}, {EncMode: "e2e"}} {
		normalizeDMEncryption(&e)
		if e.EncVerified || e.EncMode != "" {
			t.Fatalf("ambiguous new record kept a mark: %+v", e)
		}
	}
	for _, mode := range []string{"e2e", "server"} {
		e := DMEntry{EncVerified: true, EncMode: mode}
		normalizeDMEncryption(&e)
		if !e.EncVerified || e.EncMode != mode {
			t.Fatalf("valid %s record changed: %+v", mode, e)
		}
	}
}

func TestE2EPeerSetIsBounded(t *testing.T) {
	s := newE2EPeerSet()
	for i := range e2ePeerCapacity + 10 {
		s.record("peer-" + strconv.Itoa(i))
	}
	if len(s.peers) > e2ePeerCapacity {
		t.Fatalf("set holds %d peers", len(s.peers))
	}
	var nilSet *e2ePeerSet
	nilSet.record("x")
	if nilSet.has("x") {
		t.Fatal("nil set reported a peer")
	}
}

// TestDowngradedDMRoundTripsUnverified: a downgraded record (even one sent in
// as verified) reloads, searches and summarises as unverified "downgraded".
func TestDowngradedDMRoundTripsUnverified(t *testing.T) {
	_, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		return 0, nil, false
	})
	a := newLocalApp(t, cm)
	e := DMEntry{FromUniqueID: "friend", FromNickname: "mia", Body: "needle", SentAt: 5, EncVerified: true, EncMode: "downgraded"}
	if err := a.DMHistoryAppend("friend", "mia", e); err != "" {
		t.Fatalf("append: %s", err)
	}
	msgs, err := a.DMHistoryLoad("friend")
	if err != nil || len(msgs) != 1 || msgs[0].EncVerified || msgs[0].EncMode != "downgraded" {
		t.Fatalf("reloaded = %+v, %v", msgs, err)
	}
	res, err := a.DMSearch("", "needle", 0)
	if err != nil || len(res.Messages) != 1 || res.Messages[0].EncVerified || res.Messages[0].EncMode != "downgraded" {
		t.Fatalf("search = %+v, %v", res.Messages, err)
	}
	store, err := a.captureDMHistory()
	if err != nil {
		t.Fatal(err)
	}
	if store.hasE2E("friend") {
		t.Fatal("a downgraded record counted as end-to-end history")
	}
}
