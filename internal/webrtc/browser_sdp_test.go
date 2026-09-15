package webrtc

import (
	"strings"
	"testing"
	"time"
)

// Chromium rejects duplicate stream/track pairs even across audio and video
// media sections. Pion accepts them, so a Pion-only handshake misses this.
func TestRenegotiationHasDistinctMediaIdentities(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	v := NewVoice(e, r, testLogger())
	t.Cleanup(func() { _ = v.ClosePeer("subscriber") })
	clock := newManualRenegClock(time.Unix(0, 0))
	useManualRenegClock(v, clock, time.Millisecond, time.Millisecond)
	rec := &offerRecorder{}
	v.SetOfferSender(func(_ string, offer string) error { rec.add(offer); return nil })
	establishVoiceSession(t, v, newClientPC(t), "subscriber")
	r.JoinChannel(1, "subscriber")
	r.JoinChannel(1, "publisher")
	clock.Advance(time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("offers = %d, want 1", rec.count())
	}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(rec.sdp(0), "\r\n") {
		if !strings.HasPrefix(line, "a=msid:") {
			continue
		}
		if seen[line] {
			t.Errorf("browser rejects duplicate media identity %q", line)
		}
		seen[line] = true
	}
	if len(seen) < 2 {
		t.Fatal("expected microphone and camera media identities")
	}
}
