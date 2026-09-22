package webrtc

import (
	"strings"
	"testing"
	"time"
)

func TestPublisherGuardFiltersWhisperMetadataAndPrunesRevokedPairs(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "sender")
	attachFakePeer(t, e, r, "visible")
	attachFakePeer(t, e, r, "hidden")
	r.JoinChannel(1, "sender")
	r.JoinChannel(2, "visible")
	r.JoinChannel(2, "hidden")
	r.SetPublisherGuard(func(pair PublisherAccess) bool {
		return pair.PublisherID != "sender" || pair.SubscriberID == "visible"
	})
	r.SetWhisper("sender", nil, []int64{2}, true)
	if pubTrackFor(r, "hidden", "sender") != nil {
		t.Fatal("hidden whisper publisher acquired SDP tracks")
	}
	if pubTrackFor(r, "visible", "sender") == nil {
		t.Fatal("allowed whisper lost its tracks")
	}
	r.SetTrackSlots("sender", map[string]string{"display": SlotScreen})
	if pubTrackFor(r, "hidden", "sender") != nil {
		t.Fatal("slot declaration exposed hidden publisher")
	}
	r.SetPublisherGuard(func(pair PublisherAccess) bool { return pair.PublisherID != "sender" })
	if pubTrackFor(r, "visible", "sender") != nil {
		t.Fatal("revoked whisper retained SDP tracks")
	}
	r.SetPublisherGuard(func(PublisherAccess) bool { return true })
	r.PrepareSubscriber("visible")
	if pubTrackFor(r, "visible", "sender") == nil {
		t.Fatal("regrant did not restore active whisper tracks")
	}
}

func TestOfferGuardPrunesMetadataBeforeSDPCreation(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	v := NewVoice(e, r, testLogger())
	defer func() { _ = v.ClosePeer("subscriber") }()
	clock := newManualRenegClock(time.Unix(0, 0))
	useManualRenegClock(v, clock, time.Millisecond, time.Millisecond)
	establishVoiceSession(t, v, newClientPC(t), "subscriber")
	r.JoinChannel(2, "subscriber")
	r.JoinChannel(1, "hidden-publisher")
	r.SetWhisper("hidden-publisher", nil, []int64{2}, true)
	if pubTrackFor(r, "subscriber", "hidden-publisher") == nil {
		t.Fatal("fixture lacks pending whisper tracks")
	}
	guardActive := false
	rec := &offerRecorder{}
	v.SetOfferGuard(func(_ string, send func() error) error {
		if !v.renegMu.TryLock() {
			t.Fatal("offer guard entered under renegotiation lock")
		}
		v.renegMu.Unlock()
		if !r.mu.TryLock() {
			t.Fatal("offer guard entered under router lock")
		}
		r.mu.Unlock()
		r.SetPublisherGuard(func(pair PublisherAccess) bool { return pair.PublisherID != "hidden-publisher" })
		guardActive = true
		defer func() { guardActive = false }()
		return send()
	})
	v.SetOfferSender(func(_ string, offer string) error {
		if !guardActive {
			t.Error("SDP delivered after releasing its guard")
		}
		rec.add(offer)
		return nil
	})
	clock.Advance(time.Millisecond)
	if rec.count() != 1 {
		t.Fatalf("offers=%d", rec.count())
	}
	if strings.Contains(rec.sdp(0), "hidden-publisher") {
		t.Fatal("removed publisher remains in generated SDP")
	}
}
