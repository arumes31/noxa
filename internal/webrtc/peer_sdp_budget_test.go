package webrtc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

func TestCheckRemoteSDPBudget(t *testing.T) {
	var sdp strings.Builder
	sdp.WriteString("v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\n")
	for i := range maxRemoteSSRCAttributes {
		fmt.Fprintf(&sdp, "a=ssrc:%d cname:x\r\n", i+1)
	}
	if err := checkRemoteSDPBudget(sdp.String()); err != nil {
		t.Fatalf("description within budget rejected: %v", err)
	}
	sdp.WriteString("a=ssrc-group:FID 1 2\r\n")
	if err := checkRemoteSDPBudget(sdp.String()); !errors.Is(err, ErrSDPTooManySSRCs) {
		t.Fatalf("over-budget description err=%v", err)
	}
}

func TestCheckRemoteSDPBudgetCapsMediaSections(t *testing.T) {
	var sdp strings.Builder
	sdp.WriteString("v=0\r\n")
	for range maxRemoteMediaSections {
		sdp.WriteString("m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=mid:x\r\n")
	}
	if err := checkRemoteSDPBudget(sdp.String()); err != nil {
		t.Fatalf("description within budget rejected: %v", err)
	}
	sdp.WriteString("m=video 9 UDP/TLS/RTP/SAVPF 96\r\n")
	if err := checkRemoteSDPBudget(sdp.String()); !errors.Is(err, ErrSDPTooManyMediaSections) {
		t.Fatalf("over-budget description err=%v", err)
	}
	// Pion also accepts a bare CR as a line break.
	crOnly := "v=0\r" + strings.Repeat("c=IN IP4 0.0.0.0\rm=audio 9 UDP/TLS/RTP/SAVPF 111\r", maxRemoteMediaSections+1)
	if err := checkRemoteSDPBudget(crOnly); !errors.Is(err, ErrSDPTooManyMediaSections) {
		t.Fatalf("CR-separated sections slipped past the budget: %v", err)
	}
}

func TestRemoteICECandidateBudget(t *testing.T) {
	w := &PeerConnectionWrapper{logger: zap.NewNop(), remoteCandidates: make(chan pion.ICECandidateInit, 4*maxRemoteICECandidates), closed: make(chan struct{})}
	if err := w.admitDescriptionCandidates("a=ice-ufrag:one\r\n"); err != nil {
		t.Fatal(err)
	}
	add := func(i int) error {
		return w.AddICECandidate(pion.ICECandidateInit{Candidate: fmt.Sprintf("candidate:%d 1 udp 1 192.0.2.1 %d typ host", i, 1000+i)})
	}
	for i := range maxRemoteICECandidates {
		if err := add(i); err != nil {
			t.Fatalf("candidate %d rejected: %v", i, err)
		}
	}
	if err := add(0); err != nil {
		t.Fatalf("repeated candidate rejected: %v", err)
	}
	if err := w.AddICECandidate(pion.ICECandidateInit{}); err != nil {
		t.Fatalf("end-of-candidates rejected: %v", err)
	}
	if err := add(maxRemoteICECandidates); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("over-budget candidate err=%v", err)
	}
	// A re-offer repeating known candidates is free; a new one is not.
	known := "a=ice-ufrag:one\r\na=candidate:0 1 udp 1 192.0.2.1 1000 typ host\r\n"
	if err := w.admitDescriptionCandidates(known); err != nil {
		t.Fatalf("re-offer with known candidates rejected: %v", err)
	}
	if err := w.admitDescriptionCandidates(known + "a=candidate:99 1 udp 1 192.0.2.9 9 typ host\r\n"); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("re-offer grew the candidate set: %v", err)
	}
	if err := add(maxRemoteICECandidates); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("renegotiation reset the budget: %v", err)
	}
	if err := w.admitDescriptionCandidates("a=ice-ufrag:two\r\n"); err != nil { // ICE restart
		t.Fatal(err)
	}
	if err := add(maxRemoteICECandidates); err != nil {
		t.Fatalf("ICE restart kept the old budget: %v", err)
	}

	sdp := "v=0\r\n" + strings.Repeat("a=candidate:1 1 udp 1 192.0.2.1 1 typ host\r\n", maxRemoteICECandidates)
	if err := checkRemoteSDPBudget(sdp); err != nil {
		t.Fatalf("description within candidate budget rejected: %v", err)
	}
	if err := checkRemoteSDPBudget(sdp + "a=candidate:2 1 udp 1 192.0.2.2 2 typ host\r\n"); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("over-budget description err=%v", err)
	}
}
