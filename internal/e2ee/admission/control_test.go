package admission

import (
	"encoding/json"
	"strings"
	"testing"
)

func controlManifest() Manifest {
	return Manifest{Version: Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: "a", Device: "d0", AccountEpoch: 1, AttemptID: "1", LogicalID: "ctl1",
		Purpose: "control", Scope: "direct", Peer: "b", Deadline: 1900000000, Rosters: []Roster{{Principal: "a", Version: 1}, {Principal: "b", Version: 1}},
		Targets: []Target{{Principal: "a", Device: "d0", AccountEpoch: 1, Identity: "i0", Local: true}, {Principal: "b", Device: "b1", AccountEpoch: 2, Identity: "i1"}},
		Parts:   []Part{{Digest: Digest([]byte("[]")), Length: 2}}}
}

// 1c-iii-b item 3: a control names exactly one non-local device of the peer.
func TestControlManifestSingleTarget(t *testing.T) {
	m := controlManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if tg, ok := ControlTarget(m); !ok || tg.Device != "b1" {
		t.Fatal(tg, ok)
	}
	if b := ExpectedBinding(m, m.Targets[1]); b.Purpose != "media-key" {
		t.Fatalf("binding purpose %q", b.Purpose)
	}
	// The sender's own other device is a valid single target.
	own := controlManifest()
	own.Peer, own.Rosters = "a", []Roster{{Principal: "a", Version: 1}}
	own.Targets[1].Principal = "a"
	if err := own.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Manifest){
		"fanout to a second device": func(m *Manifest) {
			m.Targets = append(m.Targets, Target{Principal: "b", Device: "b2", AccountEpoch: 1, Identity: "i2"})
		},
		"own linked device beside the peer": func(m *Manifest) {
			m.Targets[1] = Target{Principal: "a", Device: "d1", AccountEpoch: 1, Identity: "i3"}
		},
		"no non-local target": func(m *Manifest) { m.Targets = m.Targets[:1] },
		"reference":           func(m *Manifest) { m.Reference = "msg" },
		"group scope": func(m *Manifest) {
			m.Scope, m.Peer, m.Conversation, m.Revision, m.Epoch = "group", "", "g", 1, 1
		},
	}
	for name, mutate := range bad {
		m := controlManifest()
		mutate(&m)
		if m.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Exactly one copy freezes and opens.
	m = controlManifest()
	parts, err := Freeze(&m, []Copy{{Principal: "b", Device: "b1", AccountEpoch: 2, SessionID: "s", Envelope: json.RawMessage(`{"x":1}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(m, parts); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
}

func TestControlBody(t *testing.T) {
	now := int64(1_800_000_000)
	ok := json.RawMessage(`{"kind":"media-key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000060,"data":{"key":"abc"}}`)
	b, err := ValidateControlSend(ok, now)
	if err != nil || b.Kind != "media-key" || b.Scope.Type != "channel" || b.Epoch != 3 {
		t.Fatal(b, err)
	}
	for name, body := range map[string]string{
		"expired":       `{"kind":"media-key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000000}`,
		"too long":      `{"kind":"media-key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000301}`,
		"unknown":       `{"kind":"media-key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000060,"x":1}`,
		"bad kind":      `{"kind":"Media Key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000060}`,
		"no scope id":   `{"kind":"media-key","scope":{"type":"channel","id":""},"epoch":3,"expires_at":1800000060}`,
		"huge data":     `{"kind":"media-key","scope":{"type":"channel","id":"42"},"epoch":3,"expires_at":1800000060,"data":"` + strings.Repeat("a", MaxControlData) + `"}`,
		"not an object": `[1]`,
	} {
		if _, err := ValidateControlSend(json.RawMessage(body), now); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// A receiver parses an expired control (to record its disposition).
	if _, err := ParseControlBody(json.RawMessage(`{"kind":"media-key","scope":{"type":"call","id":"c"},"epoch":0,"expires_at":5}`)); err != nil {
		t.Fatal(err)
	}
}
