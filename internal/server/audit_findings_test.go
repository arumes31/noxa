package server

import (
	"encoding/json"
	"testing"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

func TestMentionsNicknameMatchesWholeTokens(t *testing.T) {
	for _, tt := range []struct {
		body, nick string
		want       bool
	}{
		{"hi @bob", "bob", true},
		{"@bob, look", "bob", true},
		{"hi @bobby", "bob", false},
		{"hi @bob_2", "bob", false},
		{"@bobby and @bob", "bob", true},
		{"hi @Big Al!", "big al", true},
		{"hi bob", "bob", false},
		{"@bob", "", false},
	} {
		if got := mentionsNickname(tt.body, tt.nick); got != tt.want {
			t.Errorf("mentionsNickname(%q, %q) = %v, want %v", tt.body, tt.nick, got, tt.want)
		}
	}
}

func TestLinkHostsCanonicalizeUnicodeHosts(t *testing.T) {
	black := splitList("evil.example")
	for _, link := range []string{
		"https://evil.example/x",
		"https://EVIL.example/x",
		"https://ｅｖｉｌ.example/x", // fullwidth letters
		"https://evil。example/x", // ideographic full stop
		"https://sub.evil.example./",
	} {
		hosts := linkHosts(link)
		if len(hosts) != 1 || !hostInList(hosts[0], black) {
			t.Errorf("%q: hosts %q not matched by blacklist", link, hosts)
		}
	}
	if hosts := linkHosts("https://notevil.example/"); hostInList(hosts[0], black) {
		t.Errorf("unrelated host matched: %q", hosts)
	}
	if hosts := linkHosts("https://xn--bcher-kva.example/"); !hostInList(hosts[0], splitList("bücher.example")) {
		t.Errorf("unicode list entry did not match punycode host %q", hosts)
	}
}

func TestRecordSnapshotIgnoresGeneratedAt(t *testing.T) {
	c := &Client{}
	snap := &broadcast.TreeSnapshot{TotalClients: 1, GeneratedAt: time.Unix(1, 0)}
	if !c.recordSnapshot(snap) {
		t.Fatal("first snapshot reported unchanged")
	}
	if c.recordSnapshot(&broadcast.TreeSnapshot{TotalClients: 1, GeneratedAt: time.Unix(2, 0)}) {
		t.Fatal("snapshot differing only in GeneratedAt reported changed")
	}
	if !c.recordSnapshot(&broadcast.TreeSnapshot{TotalClients: 2}) {
		t.Fatal("changed snapshot reported unchanged")
	}
}

func TestConversationActivityDeliveryEnforcesQueueAge(t *testing.T) {
	s := &TCPServer{}
	encode := func(at time.Time) []byte {
		raw, err := json.Marshal(conversationActivityRelay[netproto.ConversationTypingEvent]{
			Event: netproto.ConversationTypingEvent{ConversationID: "g", UniqueID: "u"}, RelayedAt: at.UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var fields map[string]any
	if err := json.Unmarshal(encode(time.Now()), &fields); err != nil || fields["conversation_id"] != "g" || fields["relayed_at"] == nil {
		t.Fatalf("relay encoding = %v (%v)", fields, err)
	}
	if ok, err := s.conversationActivityDeliverable("r", encode(time.Now())); err != nil || !ok {
		t.Fatalf("fresh event: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.conversationActivityDeliverable("r", encode(time.Now().Add(-conversationActivityRevocationWindow-time.Second))); ok {
		t.Fatal("event older than the revocation window delivered")
	}
	if ok, _ := s.conversationActivityDeliverable("r", []byte(`{"conversation_id":"g"}`)); ok {
		t.Fatal("unstamped event delivered")
	}
	s.conversationActivity.revoke("r", "g", time.Now())
	if ok, _ := s.conversationActivityDeliverable("r", encode(time.Now())); ok {
		t.Fatal("event delivered after revocation")
	}
}
