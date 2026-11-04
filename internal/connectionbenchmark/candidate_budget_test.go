package connectionbenchmark

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

func TestCandidateBudgetCountsDistinctCandidatesAcrossForms(t *testing.T) {
	var b candidateBudget
	var sdp strings.Builder
	for i := range maxRemoteICECandidate / 2 {
		fmt.Fprintf(&sdp, "a=candidate:%d 1 udp 1 192.0.2.1 %d typ host\r\n", i, 1000+i)
	}
	if err := b.admitSDP(sdp.String()); err != nil {
		t.Fatal(err)
	}
	// Renegotiation re-lists the same candidates without charging them again.
	if err := b.admitSDP(sdp.String()); err != nil {
		t.Fatal(err)
	}
	for i := maxRemoteICECandidate / 2; i < maxRemoteICECandidate; i++ {
		if err := b.admit(fmt.Sprintf("candidate:%d 1 udp 1 192.0.2.1 %d typ host", i, 1000+i)); err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
	}
	if err := b.admit("candidate:0 1 udp 1 192.0.2.1 1000 typ host"); err != nil {
		t.Fatalf("trickled duplicate of an SDP candidate charged: %v", err)
	}
	if err := b.admit("candidate:999 1 udp 1 192.0.2.9 9 typ host"); !errors.Is(err, errTooManyCandidates) {
		t.Fatalf("candidate beyond budget err=%v", err)
	}
}

func TestBoundedICEServersDeduplicatesAndCaps(t *testing.T) {
	var servers []netproto.ICEServer
	for i := range 2 * maxICEServerURLs {
		servers = append(servers, netproto.ICEServer{URLs: []string{"stun:dup.example:3478", fmt.Sprintf("turn:%d.example:3478", i)}})
	}
	total := 0
	seen := map[string]bool{}
	for _, s := range boundedICEServers(servers) {
		for _, u := range s.URLs {
			if seen[u] {
				t.Fatalf("duplicate URL %s", u)
			}
			seen[u] = true
			total++
		}
	}
	if total != maxICEServerURLs {
		t.Fatalf("kept %d URLs, want %d", total, maxICEServerURLs)
	}
}
