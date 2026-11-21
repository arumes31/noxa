package webrtc

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	admitDescription := func(sdp string) error {
		w.candidateMu.Lock()
		return w.admitCandidatesLocked(descriptionCandidates(sdp)...)
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
	if err := admitDescription(known); err != nil {
		t.Fatalf("re-offer with known candidates rejected: %v", err)
	}
	if err := admitDescription(known + "a=candidate:99 1 udp 1 192.0.2.9 9 typ host\r\n"); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("re-offer grew the candidate set: %v", err)
	}
	if err := add(maxRemoteICECandidates); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("renegotiation reset the budget: %v", err)
	}

	sdp := "v=0\r\n" + strings.Repeat("a=candidate:1 1 udp 1 192.0.2.1 1 typ host\r\n", maxRemoteICECandidates)
	if err := checkRemoteSDPBudget(sdp); err != nil {
		t.Fatalf("description within candidate budget rejected: %v", err)
	}
	if err := checkRemoteSDPBudget(sdp + "a=candidate:2 1 udp 1 192.0.2.2 2 typ host\r\n"); !errors.Is(err, ErrTooManyICECandidates) {
		t.Fatalf("over-budget description err=%v", err)
	}
}

// The budget follows Pion's candidate set: it restarts only when Pion applied
// an ICE restart offer and discarded its remote candidates. A description
// Pion rejects, or an answer, leaves it unchanged.
func TestRemoteICECandidateBudgetRestartsOnlyWithPion(t *testing.T) {
	e, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	v := NewVoice(e, NewRouter(nil), testLogger())
	client := newClientPC(t)
	establishVoiceSession(t, v, client, "member")
	w := e.PeerConnection("member")
	// accepted trickles a full budget of new candidates and counts those admitted.
	accepted := func(round int) (n int) {
		t.Helper()
		for i := range maxRemoteICECandidates {
			c := fmt.Sprintf("candidate:%d 1 udp 2130706431 127.0.0.1 %d typ host", round*1000+i, 20000+round*100+i)
			// A full queue is retried: a repeated candidate is free.
			err := v.AddICECandidate("member", c, "0", 0)
			for deadline := time.Now().Add(5 * time.Second); err != nil && !errors.Is(err, ErrTooManyICECandidates) && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
				err = v.AddICECandidate("member", c, "0", 0)
			}
			if err == nil {
				n++
			} else if !errors.Is(err, ErrTooManyICECandidates) {
				t.Fatalf("candidate %d of round %d: %v", i, round, err)
			}
		}
		return n
	}
	held := func() (n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); len(w.remoteCandidates) > 0 && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond) // the last drained candidate reaches the agent
		for _, s := range w.pc.GetStats() {
			if c, ok := s.(pion.ICECandidateStats); ok && c.Type == pion.StatsTypeRemoteCandidate {
				n++
			}
		}
		return n
	}
	if got := accepted(0); got != maxRemoteICECandidates {
		t.Fatalf("first budget: %d candidates accepted", got)
	}
	for round := 1; round <= 3; round++ {
		sdp := fmt.Sprintf("a=ice-ufrag:other%d\r\n", round)
		if err := v.HandleAnswer("member", sdp); err == nil {
			t.Fatal("answer without a pending offer accepted")
		}
		if got := accepted(2*round - 1); got != 0 {
			t.Fatalf("round %d: a rejected answer admitted %d more candidates", round, got)
		}
		if _, err := v.HandleOffer("member", sdp, nil); err == nil {
			t.Fatal("malformed offer accepted")
		}
		if got := accepted(2 * round); got != 0 {
			t.Fatalf("round %d: a rejected offer admitted %d more candidates", round, got)
		}
	}
	if got := held(); got > maxRemoteICECandidates {
		t.Fatalf("Pion holds %d remote candidates, limit %d", got, maxRemoteICECandidates)
	}

	offer, err := client.CreateOffer(&pion.OfferOptions{ICERestart: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	if _, err := v.HandleOffer("member", offer.SDP, nil); err != nil {
		t.Fatalf("ICE restart offer: %v", err)
	}
	if got := accepted(10); got != maxRemoteICECandidates {
		t.Fatalf("ICE restart: %d candidates accepted, want a new budget", got)
	}
	if got := accepted(11); got != 0 {
		t.Fatalf("the new budget admitted %d candidates too many", got)
	}
	if got := held(); got > maxRemoteICECandidates {
		t.Fatalf("after ICE restart Pion holds %d remote candidates, limit %d", got, maxRemoteICECandidates)
	}
}

func TestOfferRestartsICE(t *testing.T) {
	const head = "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n"
	section := func(ufrag string) string {
		return "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=ice-ufrag:" + ufrag + "\r\n"
	}
	for name, tc := range map[string]struct {
		sdp, current string
		want         bool
	}{
		"new fragment":                  {head + section("new"), "old", true},
		"same fragment":                 {head + section("old"), "old", false},
		"current in a later section":    {head + section("new") + section("old"), "old", false},
		"current at session level":      {head + "a=ice-ufrag:old\r\n" + section("new"), "old", false},
		"current below a session decoy": {head + "a=ice-ufrag:new\r\n" + section("old"), "old", false},
		"no remote credentials yet":     {head + section("new"), "", false},
		"description Pion cannot parse": {"a=ice-ufrag:new\r\n", "old", false},
		"description without fragments": {head + "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n", "old", true},
	} {
		if got := offerRestartsICE(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: tc.sdp}, tc.current); got != tc.want {
			t.Errorf("%s: restart = %v, want %v", name, got, tc.want)
		}
	}
}
