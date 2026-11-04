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
		{"hi @bobé", "bob", false},
		{"hi @bob!", "bob", true},
		{"@bobby and @bob", "bob", true},
		{"hi @big al!", "Big Al", true},
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
	if hosts := linkHosts("see https://[2001:DB8::1]/x"); len(hosts) != 1 || hosts[0] != "2001:db8::1" || hostInList(hosts[0], black) {
		t.Errorf("IPv6 literal hosts = %q", hosts)
	}
	if hosts := linkHosts("https://[2001:db8::1]:8443/"); !hostInList(hosts[0], splitList("2001:db8::1")) {
		t.Errorf("blacklisted IPv6 literal not matched: %q", hosts)
	}
	if hosts := linkHosts("https://xn--bcher-kva.example/"); !hostInList(hosts[0], splitList("bücher.example")) {
		t.Errorf("unicode list entry did not match punycode host %q", hosts)
	}
}

func TestSnapshotDigestCommitsOnlyAfterWrite(t *testing.T) {
	c := &Client{}
	first := &broadcast.TreeSnapshot{TotalClients: 1, GeneratedAt: time.Unix(1, 0)}
	if c.snapshotUnchanged(first) {
		t.Fatal("unsent snapshot reported unchanged")
	}
	c.stageSnapshot(first)
	if c.snapshotUnchanged(first) {
		t.Fatal("staged but unwritten snapshot reported unchanged")
	}
	c.commitStagedSnapshot()
	if !c.snapshotUnchanged(&broadcast.TreeSnapshot{TotalClients: 1, GeneratedAt: time.Unix(2, 0)}) {
		t.Fatal("snapshot differing only in GeneratedAt reported changed")
	}
	if c.snapshotUnchanged(&broadcast.TreeSnapshot{TotalClients: 2}) {
		t.Fatal("changed snapshot reported unchanged")
	}
	c.recordSnapshot(&broadcast.TreeSnapshot{TotalClients: 3})
	if !c.snapshotUnchanged(&broadcast.TreeSnapshot{TotalClients: 3}) {
		t.Fatal("recorded snapshot not remembered")
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
	s.conversationActivity.revoke("r", "g", time.Now())
	if ok, _ := s.conversationActivityDeliverable("r", encode(time.Now())); ok {
		t.Fatal("event delivered after revocation")
	}
}
