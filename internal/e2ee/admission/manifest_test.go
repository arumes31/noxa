package admission

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompleteImmutableWire(t *testing.T) {
	for _, n := range []int{20, 160} {
		m := Manifest{Version: Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: "a", Device: "d0", AccountEpoch: 1, AttemptID: "1", LogicalID: "msg1", Purpose: "chat", Scope: "direct", Peer: "b", Deadline: 1900000000, Rosters: []Roster{{Principal: "a", Version: 1}, {Principal: "b", Version: 1}}}
		var copies []Copy
		for i := 0; i < n; i++ {
			id := fmt.Sprint("d", i)
			p := "b"
			if i == 0 {
				p = "a"
			}
			m.Targets = append(m.Targets, Target{Principal: p, Device: id, AccountEpoch: 1, Identity: "identity", Local: i == 0})
			if i != 0 {
				copies = append(copies, Copy{Principal: p, Device: id, AccountEpoch: 1, SessionID: "session", Envelope: json.RawMessage(`{"body":"` + strings.Repeat("a", 20000) + `"}`)})
			}
		}
		parts, e := Freeze(&m, copies)
		if e != nil {
			t.Fatal(n, e)
		}
		for _, p := range parts {
			if len(p) > MaxPartBytes {
				t.Fatal("frame bound")
			}
		}
		got, e := Open(m, parts)
		if e != nil || len(got) != n-1 {
			t.Fatal(n, e, len(got))
		}
		parts[0][0] ^= 1
		if _, e = Open(m, parts); e == nil {
			t.Fatal("altered part accepted")
		}
	}
}
func TestManifestRejectsControlBypass(t *testing.T) {
	m := Manifest{Version: Version, Server: "tls-sha256:" + strings.Repeat("a", 64), Sender: "a", Device: "d", AccountEpoch: 1, AttemptID: "1", LogicalID: "msg", Purpose: "media-key", Scope: "direct", Peer: "b", Deadline: 1900000000, Rosters: []Roster{{Principal: "a", Version: 1}, {Principal: "b", Version: 1}}, Targets: []Target{{Principal: "a", Device: "d", AccountEpoch: 1, Identity: "key", Local: true}}}
	if _, e := Freeze(&m, nil); e == nil {
		t.Fatal("control accepted through chat admission")
	}
	m.Purpose = "chat"
	m.Targets = append(m.Targets, m.Targets[0])
	if _, e := Freeze(&m, nil); e == nil {
		t.Fatal("duplicate local coverage")
	}
}
