package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
)

func TestNativeStallReason(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	connected := nativeStallState{Offered: t0, Connected: at(1)}
	cases := []struct {
		name  string
		state nativeStallState
		now   time.Time
		stall bool
	}{
		{"no offer yet", nativeStallState{}, at(60), false},
		{"connecting within 15 s", nativeStallState{Offered: t0}, at(14.9), false},
		{"never connected", nativeStallState{Offered: t0}, at(15), true},
		{"never connected but paused", nativeStallState{Offered: t0, Idle: true}, at(60), false},
		{"connected, no video yet", connected, at(10.9), false},
		{"connected, no video for 10 s", connected, at(11), true},
		{"video flowing", nativeStallState{Offered: t0, Connected: at(1), LastVideo: at(29)}, at(30), false},
		{"video stopped", nativeStallState{Offered: t0, Connected: at(1), LastVideo: at(20)}, at(30), true},
		{"minimized or paused", nativeStallState{Offered: t0, Connected: at(1), Idle: true}, at(60), false},
		{"resumed recently", nativeStallState{Offered: t0, Connected: at(1), Resumed: at(55)}, at(60), false},
		{"resumed long ago", nativeStallState{Offered: t0, Connected: at(1), Resumed: at(45)}, at(60), true},
		{"already fired", nativeStallState{Offered: t0, Fired: true}, at(60), false},
	}
	for _, c := range cases {
		if got := nativeStallReason(c.state, c.now); (got != "") != c.stall {
			t.Errorf("%s: reason %q, want stall %v", c.name, got, c.stall)
		}
	}
}

func TestNativeStatsSchedule(t *testing.T) {
	want := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 70 * time.Second, 130 * time.Second}
	for i, w := range want {
		if got := nativeStatsAt(i); got != w {
			t.Errorf("nativeStatsAt(%d) = %v, want %v", i, got, w)
		}
	}
}

func TestNativeShareSnapshotLine(t *testing.T) {
	s := nativeShareSnapshot{elapsed: 5200 * time.Millisecond, peer: "connected", upload: true, generation: 3,
		cs:      nativecapture.Stats{Captured: 150, Converted: 149, Submitted: 148, NeedInput: 149, Encoded: 0},
		ss:      nativesender.Stats{AudioFramesSent: 250},
		encoder: nativecapture.EncoderInfo{Name: "AMD H.264 Encoder MFT", Vendor: "amd", Hardware: true},
		dropped: 2, last: "dropped: native frame dropped"}
	want := `t=5s peer=connected upload=on gen=3 minimized=false captured=150 converted=149 submitted=148 needinput=149 encoded=0 keyframes=0` +
		` unsent=0 rejected=0 dropped=2 nokey=0 sent=0 keysent=0 rtp=0 wire=0 audio=250/0 encoder="AMD H.264 Encoder MFT"/amd/hw last="dropped: native frame dropped"`
	if got := s.String(); got != want {
		t.Fatalf("line\n got %s\nwant %s", got, want)
	}
}

func TestNativeFrameDiagLogsEachReasonOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })
	log.SetOutput(&buf)
	var d nativeFrameDiag
	noKey := errors.Join(nativesender.ErrDropped, framecrypt.ErrNoKey)
	multi := errors.New("framecrypt: more than one slice in access unit")
	for range 3 {
		d.note(dropKind(noKey), noKey)
		d.note(dropKind(multi), multi)
	}
	if d.dropped.Load() != 3 || d.noKey.Load() != 3 || d.rejected.Load() != 3 {
		t.Fatalf("dropped %d rejected %d", d.dropped.Load(), d.rejected.Load())
	}
	out := buf.String()
	if strings.Count(out, "\n") != 2 || !strings.Contains(out, "first dropped: native frame dropped; framecrypt: no active media key") ||
		!strings.Contains(out, "first rejected: framecrypt: more than one slice") {
		t.Fatalf("log:\n%s", out)
	}
	if d.last != "rejected: "+multi.Error() {
		t.Fatalf("last = %q", d.last)
	}
}

func TestNativeStallIdleFollowsTheCurrentKey(t *testing.T) {
	cases := []struct {
		name                         string
		upload, minimized, keyActive bool
		generation                   uint64
		idle                         bool
	}{
		{"uploading with a key", true, false, true, 3, false},
		{"no active video key", true, false, false, 3, true},
		{"paused", false, false, true, 3, true},
		{"not yet published", true, false, true, 0, true},
		{"minimized", true, true, true, 3, true},
	}
	for _, c := range cases {
		if got := nativeStallIdle(c.upload, c.generation, c.minimized, c.keyActive); got != c.idle {
			t.Errorf("%s: idle %v, want %v", c.name, got, c.idle)
		}
	}
	// A key that arrived after an earlier no-key drop keeps the watchdog armed:
	// video that then stops is a stall, whatever the noKey counter did.
	keys := framecrypt.NewEncryptor()
	n := &nativeShare{keys: keys}
	if _, ok := n.keys.Active(); ok {
		t.Fatal("key active before any key")
	}
	if err := keys.SetKey(1, 1, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := keys.Activate(1); err != nil {
		t.Fatal(err)
	}
	_, ok := n.keys.Active()
	t0 := time.Unix(1000, 0)
	state := nativeStallState{Offered: t0, Connected: t0, LastVideo: t0, Idle: nativeStallIdle(true, 3, false, ok)}
	if nativeStallReason(state, t0.Add(nativeVideoTimeout)) == "" {
		t.Fatal("video stopped with an active key, but no stall")
	}
}

func TestNativeStallDecisionIsRevalidated(t *testing.T) {
	session, sender := &nativecapture.Session{}, &nativesender.Sender{}
	n := &nativeShare{stop: make(chan struct{}), session: session, sender: sender, upload: true, generation: 4}
	seen := nativeObserved{session: session, sender: sender, upload: true, generation: 4}
	for name, change := range map[string]func(){
		"sender replaced":   func() { n.sender = &nativesender.Sender{} },
		"session restarted": func() { n.session = &nativecapture.Session{} },
		"upload paused":     func() { n.upload = false },
		"new publication":   func() { n.generation = 5 },
	} {
		n.session, n.sender, n.upload, n.generation = session, sender, true, 4
		change()
		// n.cm is nil: committing the stale decision would panic in close.
		if n.failIfCurrent(seen, "stalled") || n.failed.Load() {
			t.Fatalf("%s: stale stall decision committed", name)
		}
	}
}
