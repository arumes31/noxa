package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/netproto"
)

const metaTestKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func TestAttachmentMetaRoundTrip(t *testing.T) {
	peaks := strings.Repeat("Az09_-", 8) // 48 levels
	for _, meta := range []attachmentMeta{
		{Size: 1},
		{Size: 1_234_567},
		{Size: 9000, DurationMS: 14_000, Peaks: peaks},
		{DurationMS: 1},
	} {
		token := formatFileRef("0123456789abcdef0123456789abcdef.vcx", metaTestKey, meta, "voice-1.weba")
		capture := strings.TrimSuffix(strings.TrimPrefix(token, "[file:"), "]")
		storage, key, name, got := splitFileRef(capture)
		if storage != "0123456789abcdef0123456789abcdef.vcx" || key != metaTestKey || name != "voice-1.weba" || got != meta {
			t.Fatalf("%+v -> %q -> %q %q %q %+v", meta, token, storage, key, name, got)
		}
		if s, k, n := parseFileRef(capture); s != storage || k != key || n != name {
			t.Fatalf("parseFileRef(%q) = %q %q %q", capture, s, k, n)
		}
	}
}

func TestAttachmentMetaBackwardCompatibility(t *testing.T) {
	// Without metadata the token is byte-for-byte the legacy form.
	if got := formatFileRef("aa.vcx", metaTestKey, attachmentMeta{}, "a#b]c.png"); got != "[file:aa.vcx#"+metaTestKey+"#a_b_c.png]" {
		t.Fatalf("legacy token = %q", got)
	}
	// Old messages: no metadata, same parts as before.
	for capture, want := range map[string][3]string{
		"photo.png":                        {"photo.png", "", "photo.png"},
		"aa.vcx#" + metaTestKey + "#x.pdf": {"aa.vcx", metaTestKey, "x.pdf"},
		"aa.vcx#onlyone":                   {"aa.vcx#onlyone", "", "aa.vcx#onlyone"},
	} {
		s, k, n, meta := splitFileRef(capture)
		if [3]string{s, k, n} != want || !meta.empty() {
			t.Fatalf("splitFileRef(%q) = %q %q %q %+v", capture, s, k, n, meta)
		}
	}
	// What an OLDER client sees in a new token: storage and key are intact,
	// so it still downloads and decrypts; only the shown name is longer.
	token := formatFileRef("aa.vcx", metaTestKey, attachmentMeta{Size: 42}, "report.pdf")
	s, k, n := parseFileRefParts(strings.TrimSuffix(strings.TrimPrefix(token, "[file:"), "]"))
	if s != "aa.vcx" || k != metaTestKey || n != "nx1;s=42#report.pdf" {
		t.Fatalf("legacy parse of new token = %q %q %q", s, k, n)
	}
	// A legacy plain reference never grows metadata, even if it looks like it.
	if _, k, n, meta := splitFileRef("nx1;s=5#photo.png"); k != "" || n != "nx1;s=5#photo.png" || !meta.empty() {
		t.Fatalf("plain reference parsed as metadata: %q %+v", n, meta)
	}
}

func TestAttachmentMetaRejectsMalformedSegments(t *testing.T) {
	for _, segment := range []string{"", "nx1;", "nx2;s=4", "nx1;S=4", "nx1;s=4 ", "nx1;s=", "nx1;w=a/b", "x;s=1", "nx1;z=" + strings.Repeat("a", 300)} {
		if _, ok := parseAttachmentMeta(segment); ok {
			t.Fatalf("accepted %q", segment)
		}
	}
	// Well-formed but out of range values are dropped field by field, and
	// unknown fields are ignored for forward compatibility.
	meta, ok := parseAttachmentMeta("nx1;s=0;d=99999999999;z=future;w=" + strings.Repeat("A", 65))
	if !ok || !meta.empty() {
		t.Fatalf("out-of-range meta = %+v ok=%v", meta, ok)
	}
	if got := (attachmentMeta{Size: -1, DurationMS: -1, Peaks: "bad peaks"}).encode(); got != "" {
		t.Fatalf("invalid meta encoded as %q", got)
	}
	// An unparseable segment stays part of the display name, so nothing is lost.
	if _, _, name, meta := splitFileRef("aa.vcx#" + metaTestKey + "#nx1;s=1 2#x.pdf"); name != "nx1;s=1 2#x.pdf" || !meta.empty() {
		t.Fatalf("malformed segment = %q %+v", name, meta)
	}
}

// The metadata travels only inside the sealed body. A private-group envelope
// and a DM box both carry it intact to the recipient, and the ciphertext the
// server stores or relays does not contain it.
func TestAttachmentMetaSealedRoundTrip(t *testing.T) {
	senderPub, senderPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientPub, recipientPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := attachmentMeta{Size: 48_213, DurationMS: 14_250, Peaks: strings.Repeat("Mq", 24)}
	token := formatFileRef("0123456789abcdef0123456789abcdef.vcx", metaTestKey, want, "voice-1.weba")

	plain, err := json.Marshal(conversationPlaintext{ConversationID: "group-a", Epoch: 2, Reference: "ref", From: "alice", To: "bob", Text: token})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealDM(string(plain), *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(sealed)
	if strings.Contains(sealed, "nx1") || strings.Contains(string(raw), "nx1") || strings.Contains(string(raw), "48213") {
		t.Fatal("metadata visible in the sealed group envelope")
	}
	text, err := openConversation(netproto.ConversationMessage{ConversationID: "group-a", Epoch: 2, Reference: "ref", FromUniqueID: "alice", Body: sealed}, "bob", *senderPub, *recipientPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, name, got := splitFileRef(strings.TrimSuffix(strings.TrimPrefix(text, "[file:"), "]")); got != want || name != "voice-1.weba" {
		t.Fatalf("group round trip = %q %+v", name, got)
	}

	dm, err := sealDM(token, *recipientPub, *senderPriv)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openDM(dm, *senderPub, *recipientPriv)
	if err != nil || opened != token {
		t.Fatalf("dm round trip = %q, %v", opened, err)
	}
}

// activityFrames records what the client wrote, answering conversation
// requests with an empty result.
type activityFrames struct {
	mu       sync.Mutex
	typing   []netproto.Typing
	requests []netproto.ConversationRequest
}

func (f *activityFrames) handle(frame *netproto.Frame) (netproto.MessageType, any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch netproto.MessageType(frame.Type) {
	case netproto.MsgTyping:
		var typing netproto.Typing
		_ = netproto.Decode(frame, &typing)
		f.typing = append(f.typing, typing)
		return 0, nil, false
	case netproto.MsgConversationRequest:
		var request netproto.ConversationRequest
		_ = netproto.Decode(frame, &request)
		f.requests = append(f.requests, request)
		return netproto.MsgConversationResult, netproto.ConversationResult{Action: request.Action}, true
	}
	return 0, nil, false
}

func (f *activityFrames) snapshot() ([]netproto.Typing, []netproto.ConversationRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]netproto.Typing(nil), f.typing...), append([]netproto.ConversationRequest(nil), f.requests...)
}

func TestConversationActivityIsGatedByCapabilityAndPrivacySetting(t *testing.T) {
	const group = "ce7189ed-d3d5-442b-b147-241ea9cb3fb9"
	frames := &activityFrames{}
	app, cm := newPipedApp(t, frames.handle)
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	read := netproto.ConversationRequest{Action: "mark_read", ID: group, ReadMessageID: 4, ShareRead: true}

	// An older server: no group typing frame (it would turn global) and no
	// receipt request, whatever the frontend asks for.
	if got := app.SendConversationTypingForTab("a", group); got != "" {
		t.Fatalf("typing without capability = %q", got)
	}
	if _, err := app.ConversationForTab("a", read); err != nil {
		t.Fatal(err)
	}

	cm.mu.Lock()
	cm.supportsConversationActivity = true
	cm.mu.Unlock()
	if got := app.SendConversationTypingForTab("a", group); got != "" {
		t.Fatalf("typing = %q", got)
	}
	read.ShareRead = false
	if _, err := app.ConversationForTab("a", read); err != nil {
		t.Fatal(err)
	}

	app.settingsMu.Lock()
	app.settings.HideChatActivity = true
	app.settingsMu.Unlock()
	if got := app.SendConversationTypingForTab("a", group); got != "" {
		t.Fatalf("typing while hidden = %q", got)
	}
	if got := app.SendTypingForTab("a", 0, "peer"); got != "" {
		t.Fatalf("dm typing while hidden = %q", got)
	}
	if got := app.SendChatReadForTab("a", "peer", "cmid"); got != "" {
		t.Fatalf("dm read while hidden = %q", got)
	}
	read.ShareRead = true
	if _, err := app.ConversationForTab("a", read); err != nil {
		t.Fatal(err)
	}
	if got := app.SendConversationTypingForTab("a", "not-a-group"); got == "" {
		t.Fatal("accepted a malformed group id")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		typing, requests := frames.snapshot()
		if len(requests) == 3 && len(typing) == 1 {
			if typing[0] != (netproto.Typing{ConversationID: group}) {
				t.Fatalf("typing frame = %+v", typing[0])
			}
			if requests[0].ShareRead || !requests[1].ShareRead || requests[2].ShareRead {
				t.Fatalf("share_read = %v %v %v, want false (old server) true (opted in) false (hidden)",
					requests[0].ShareRead, requests[1].ShareRead, requests[2].ShareRead)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("typing=%+v requests=%+v", typing, requests)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
