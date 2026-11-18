package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/nativesender/sendertest"
	"noxa/internal/netproto"
)

const testCallID = "8f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77"

func TestCallDescriptionShape(t *testing.T) {
	cases := []struct {
		d    PrivateCallDescription
		want bool
	}{
		{PrivateCallDescription{Type: "offer"}, true},
		{PrivateCallDescription{Type: "answer", Features: callFeatures}, true},
		{PrivateCallDescription{Type: "offer", Sub: callSubScreen}, true},
		{PrivateCallDescription{Type: "candidates", Sub: callSubScreen}, true},
		{PrivateCallDescription{Type: "answer", Sub: callSubScreenReply}, true},
		{PrivateCallDescription{Type: "candidates", Sub: callSubScreenReply}, true},
		// The viewer never offers, the sharer never answers.
		{PrivateCallDescription{Type: "answer", Sub: callSubScreen}, false},
		{PrivateCallDescription{Type: "offer", Sub: callSubScreenReply}, false},
		{PrivateCallDescription{Type: "offer", Sub: "camera"}, false},
		// Features belong to the main connection only, bounded.
		{PrivateCallDescription{Type: "offer", Sub: callSubScreen, Features: callFeatures}, false},
		{PrivateCallDescription{Type: "offer", Features: make([]string, 9)}, false},
		{PrivateCallDescription{Type: "offer", Features: []string{""}}, false},
		{PrivateCallDescription{Type: "offer", Features: []string{string(make([]byte, 33))}}, false},
	}
	for i, tc := range cases {
		if got := validCallDescriptionShape(tc.d); got != tc.want {
			t.Errorf("case %d %+v: %v, want %v", i, tc.d, got, tc.want)
		}
	}
}

func TestCallRelayServersAreRelayOnly(t *testing.T) {
	servers := []netproto.ICEServer{
		{URLs: []string{"stun:stun.example:3478"}},
		{URLs: []string{"turn:relay.example:3478", "stun:relay.example:3478"}, Username: "u", Credential: "c"},
		{URLs: []string{"TURNS:relay.example:5349"}, Username: "u", Credential: "c"},
		{URLs: []string{"turn:nocreds.example:3478"}},
	}
	got, err := callRelayServers(servers)
	if err != nil || len(got) != 2 || got[0].URLs[0] != "turn:relay.example:3478" || got[1].URLs[0] != "TURNS:relay.example:5349" {
		t.Fatalf("relay servers: %+v, %v", got, err)
	}
	got[0].URLs[0] = "changed"
	if servers[1].URLs[0] != "turn:relay.example:3478" {
		t.Fatal("the cached ICE configuration was mutated")
	}
	if _, err := callRelayServers(servers[:1]); err == nil {
		t.Fatal("a call without TURN must not fall back to direct candidates")
	}
}

func TestCallKeysBelongToOneCall(t *testing.T) {
	m := &connManager{}
	video := m.callKeys(testCallID, "screen")
	if m.callKeys(testCallID, "") != video || m.callKeys(testCallID, "screenaudio") == video {
		t.Fatal("slots of one call")
	}
	if m.callKeys("9f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77", "screen") == video {
		t.Fatal("another call must not reuse this call's keys")
	}
	if m.nativeKeys("screen") == video {
		t.Fatal("call keys must not be the channel's")
	}
}

// testAU is an encoder access unit (AUD, SPS/PPS on keyframes, one slice).
func testAU(key bool, payload []byte) []byte {
	sps := []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20}
	pps := []byte{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}
	au := []byte{0, 0, 0, 1, 0x09, 0x10}
	slice := []byte{0x41, 0x9a}
	if key {
		au = append(append(append(au, 0, 0, 0, 1), sps...), 0, 0, 0, 1)
		au = append(au, pps...)
		slice = []byte{0x65, 0x88}
	}
	return append(append(append(au, 0, 0, 1), slice...), payload...)
}

// callViewer is an in-process viewer of the screen sub-connection: each
// offer gets a fresh receiving peer (like the webview's ScreenSubReceiver),
// its answer and candidates come back as sealed-description replies.
type callViewer struct {
	t      *testing.T
	call   *nativeCall
	mu     sync.Mutex
	recv   []*sendertest.Receiver
	sent   []PrivateCallDescription
	offers chan struct{}
}

func (v *callViewer) send(uid string, d PrivateCallDescription) error {
	if uid != "viewer" || d.Sub != callSubScreen || !validCallDescriptionShape(d) || !validPrivateCallPayload(d.Type, d.SDP) {
		v.t.Errorf("invalid sub-connection signal to %s: %+v", uid, d)
		return errors.New("invalid")
	}
	v.mu.Lock()
	v.sent = append(v.sent, d)
	v.mu.Unlock()
	switch d.Type {
	case "offer":
		r := sendertest.NewReceiver(v.t)
		r.SetSender(viewerCandidates{v})
		answer, err := r.Offer(netproto.WebRTCOffer{SDP: d.SDP})
		if err != nil {
			return err
		}
		v.mu.Lock()
		v.recv = append(v.recv, r)
		v.mu.Unlock()
		v.call.reply(PrivateCallDescription{CallID: testCallID, From: "viewer", Type: "answer", SDP: answer.SDP, Sub: callSubScreenReply})
		v.offers <- struct{}{}
	case "candidates":
		var list []callCandidate
		if err := json.Unmarshal([]byte(d.SDP), &list); err != nil || len(list) == 0 || len(list) > 16 {
			v.t.Errorf("candidates: %s", d.SDP)
			return err
		}
		r := v.current()
		for _, c := range list {
			if err := r.PC.AddICECandidate(webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: c.SDPMid, SDPMLineIndex: c.SDPMLineIndex}); err != nil {
				v.t.Errorf("add candidate: %v", err)
			}
		}
	}
	return nil
}

func (v *callViewer) current() *sendertest.Receiver {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.recv[len(v.recv)-1]
}

type viewerCandidates struct{ v *callViewer }

func (c viewerCandidates) AddCandidate(candidate netproto.ICECandidate) error {
	mid, index := candidate.SDPMid, candidate.SDPMLineIndex
	payload, _ := json.Marshal([]callCandidate{{Candidate: candidate.Candidate, SDPMid: &mid, SDPMLineIndex: &index}})
	c.v.call.reply(PrivateCallDescription{CallID: testCallID, From: "viewer", Type: "candidates", SDP: string(payload), Sub: callSubScreenReply})
	return nil
}

func testCallShare(t *testing.T) (*nativeShare, *callViewer) {
	cm := &connManager{uniqueID: "me"}
	n := &nativeShare{cm: cm, keys: cm.callKeys(testCallID, "screen"), audioKeys: cm.callKeys(testCallID, "screenaudio"),
		stop: make(chan struct{}), upload: true, generation: 1}
	top := nativesender.Level{MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 2_000_000}
	n.ladder = nativesender.NewLadder(top, top.Bitrate)
	n.level = n.ladder.Current()
	n.call = &nativeCall{share: n, callID: testCallID, peers: map[string]*nativeCallPeer{}, settings: sendertest.LoopbackSettings()}
	viewer := &callViewer{t: t, call: n.call, offers: make(chan struct{}, 4)}
	n.call.sendTo = viewer.send
	cm.nativeShare = n
	t.Cleanup(n.close)
	return n, viewer
}

func waitConnected(t *testing.T, c *nativeCall) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.senders(); len(s) == 1 && s[0].ConnectionState() == webrtc.PeerConnectionStateConnected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("screen sub-connection did not connect")
}

// streamAndDecrypt sends frames on the share and decrypts them at the viewer
// with the call's key material.
func streamAndDecrypt(t *testing.T, n *nativeShare, r *sendertest.Receiver, material []byte, start time.Time) {
	t.Helper()
	var want [][]byte
	for i := range 12 {
		key := i%6 == 0
		payload := make([]byte, 200+i*400)
		_, _ = rand.Read(payload)
		au := testAU(key, payload)
		canonical, _, err := framecrypt.CanonicalH264(au, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, canonical)
		n.onFrame(nativecapture.Frame{Data: au, Keyframe: key, Captured: start.Add(time.Duration(i) * 16 * time.Millisecond)})
	}
	for i := range want {
		select {
		case frame := <-r.Frames:
			if got := sendertest.Decrypt(t, material, frame); !bytes.Equal(got, want[i]) {
				t.Fatalf("frame %d differs after decrypt", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d not received", i)
		}
	}
}

func TestNativeCallShareStreamsEncryptedToViewerAndReconnects(t *testing.T) {
	n, viewer := testCallShare(t)
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	if err := n.keys.SetKey(7, 2, material); err != nil {
		t.Fatal(err)
	}
	if err := n.keys.Activate(7); err != nil {
		t.Fatal(err)
	}
	if err := n.call.setTargets([]string{"viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := n.call.open(); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, n.call)
	streamAndDecrypt(t, n, viewer.current(), material, time.Now())

	// The sub-connection's signals: offer first, candidates after it, all sub "screen".
	viewer.mu.Lock()
	first := viewer.sent[0]
	viewer.mu.Unlock()
	if first.Type != "offer" {
		t.Fatalf("first signal %q, want the offer", first.Type)
	}

	// A failed connection is offered again; the new one streams too.
	n.call.mu.Lock()
	p := n.call.peers["viewer"]
	link := p.link
	n.call.mu.Unlock()
	n.call.lost(p, link)
	select {
	case <-viewer.offers: // the first offer
	default:
	}
	select {
	case <-viewer.offers:
	case <-time.After(5 * time.Second):
		t.Fatal("no new offer after the connection was lost")
	}
	waitConnected(t, n.call)
	streamAndDecrypt(t, n, viewer.current(), material, time.Now().Add(time.Second))

	// A viewer who left loses its sub-connection; nothing is sent to it.
	if err := n.call.setTargets(nil); err != nil {
		t.Fatal(err)
	}
	if len(n.call.senders()) != 0 {
		t.Fatal("a removed viewer keeps its sender")
	}
	n.close()
	n.close() // idempotent
}

func TestNativeCallShareTargetsAndReplies(t *testing.T) {
	n, _ := testCallShare(t)
	if err := n.call.setTargets([]string{"me"}); err == nil {
		t.Fatal("the sharer is no viewer of its own share")
	}
	if err := n.call.setTargets([]string{""}); err == nil {
		t.Fatal("empty viewer")
	}
	if err := n.call.open(); err == nil {
		t.Fatal("a share without viewers cannot start")
	}
	// Replies for another call, or from a non-viewer, are ignored.
	n.cm.routeCallScreenReply(PrivateCallDescription{CallID: "other", From: "viewer", Type: "answer", SDP: "x", Sub: callSubScreenReply})
	n.call.reply(PrivateCallDescription{CallID: testCallID, From: "stranger", Type: "answer", SDP: "x", Sub: callSubScreenReply})
	// Candidates that arrive before the sender exists are held, bounded at 64.
	link := &nativeCallLink{call: n.call, answers: make(chan string, 1)}
	p := &nativeCallPeer{uid: "viewer", link: link}
	link.peer = p
	n.call.peers["viewer"] = p
	entry, _ := json.Marshal(make([]callCandidate, 16))
	for range 5 {
		n.call.reply(PrivateCallDescription{CallID: testCallID, From: "viewer", Type: "candidates", SDP: string(entry), Sub: callSubScreenReply})
	}
	if len(link.remote) != 64 {
		t.Fatalf("held %d candidates, want 64", len(link.remote))
	}
	n.cm.routeCallScreenReply(PrivateCallDescription{CallID: testCallID, From: "viewer", Type: "answer", SDP: "answer-sdp", Sub: callSubScreenReply})
	if got := <-link.answers; got != "answer-sdp" {
		t.Fatalf("answer %q", got)
	}
}
