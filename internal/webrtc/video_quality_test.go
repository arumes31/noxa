package webrtc

import (
	"sync"
	"testing"

	"github.com/pion/rtcp"
)

func TestStreamVideoQualityConcurrentWatchChanges(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	session := r.VideoWatchSession("sub")
	var workers sync.WaitGroup
	workers.Go(func() {
		for i := range 200 {
			// A stop may win the lock; rejected requests must leave no state.
			_ = r.SetStreamVideoQuality("sub", "pub", SlotScreen, generation, session, []string{"high", "mid", "low"}[i%3])
		}
	})
	workers.Go(func() {
		for i := uint64(2); i <= 200; i++ {
			if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, i, session, i%2 == 0); err != nil {
				t.Error(err)
				return
			}
		}
	})
	workers.Go(func() {
		for range 200 {
			r.mu.RLock()
			rid := r.streamLayerPrefLocked("sub", "pub", SlotScreen)
			r.mu.RUnlock()
			if rid != "f" && rid != "h" && rid != "q" {
				t.Errorf("invalid layer %q", rid)
				return
			}
		}
	})
	workers.Wait()
	r.DetachPeer("sub")
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if len(r.watches) != 0 {
		t.Fatal("concurrent changes survived teardown")
	}
}

func TestStreamVideoQualityIndependentPublishersAndSlots(t *testing.T) {
	r := NewRouter(nil)
	for _, id := range []string{"viewer", "other", "a", "b"} {
		r.JoinChannel(1, id)
	}
	session := r.VideoWatchSession("viewer")
	generations := make(map[publicationKey]uint64)
	for _, publisher := range []string{"a", "b"} {
		for _, slot := range []string{SlotCam, SlotScreen} {
			generations[publicationKey{publisher, slot}] = testVideoPublication(t, r, publisher, "viewer", slot)
			testVideoPublication(t, r, publisher, "other", slot)
			for i, rid := range []string{"f", "h", "q"} {
				registerVideoSource(r, publisher, slot, rid, uint32(100+i))
			}
		}
	}
	set := func(publisher, slot, quality string) {
		t.Helper()
		if err := r.SetStreamVideoQuality("viewer", publisher, slot, generations[publicationKey{publisher, slot}], session, quality); err != nil {
			t.Fatal(err)
		}
	}
	assertLayer := func(viewer, publisher, slot, want string) {
		t.Helper()
		r.mu.RLock()
		defer r.mu.RUnlock()
		if got := r.preferredRIDLocked(viewer, publisher, slot); got != want {
			t.Fatalf("%s receiving %s/%s = %q, want %q", viewer, publisher, slot, got, want)
		}
	}
	set("a", SlotScreen, "high")
	set("b", SlotScreen, "high")
	set("a", SlotCam, "low")
	set("b", SlotCam, "mid")
	assertLayer("viewer", "a", SlotScreen, "f")
	assertLayer("viewer", "b", SlotScreen, "f")
	assertLayer("viewer", "a", SlotCam, "q")
	assertLayer("viewer", "b", SlotCam, "h")
	assertLayer("other", "a", SlotScreen, "h")
	if err := r.SetVideoQuality("viewer", "low"); err != nil {
		t.Fatal(err)
	}
	assertLayer("viewer", "a", SlotScreen, "f")
	assertLayer("viewer", "b", SlotScreen, "f")
	set("a", SlotScreen, "default")
	assertLayer("viewer", "a", SlotScreen, "q")
	assertLayer("viewer", "b", SlotScreen, "f")
	// An absent high layer falls back independently to mid for only this slot.
	r.mu.Lock()
	delete(r.videoSources["b"][SlotScreen], "f")
	r.mu.Unlock()
	assertLayer("viewer", "b", SlotScreen, "h")
}

func TestStreamVideoQualityRejectsStaleUnauthorizedAndMalformedTargets(t *testing.T) {
	for _, scenario := range []string{"slot", "quality", "generation", "session", "missing", "not watching", "forbidden", "channel", "rebuild", "republish"} {
		t.Run(scenario, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "sub")
			generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
			session := r.VideoWatchSession("sub")
			publisher, slot, quality := "pub", SlotScreen, "high"
			switch scenario {
			case "slot":
				slot = SlotMic
			case "quality":
				quality = "ultra"
			case "generation":
				generation++
			case "session":
				session++
			case "missing":
				publisher = "unknown"
			case "not watching":
				if _, err := r.WatchVideo("sub", "pub", slot, generation, 2, session, false); err != nil {
					t.Fatal(err)
				}
			case "forbidden":
				r.SetPublisherGuard(func(PublisherAccess) bool { return false })
			case "channel":
				r.JoinChannel(2, "sub")
			case "rebuild":
				r.DetachPeerKeepChannel("sub")
			case "republish":
				if _, err := r.PublishVideo("pub", slot, generation, false); err != nil {
					t.Fatal(err)
				}
				testVideoPublication(t, r, "pub", "sub", slot)
			}
			if err := r.SetStreamVideoQuality("sub", publisher, slot, generation, session, quality); err == nil {
				t.Fatal("invalid request succeeded")
			}
			r.mu.RLock()
			defer r.mu.RUnlock()
			if r.layerPrefLocked("sub") != "h" {
				t.Fatal("scoped request changed global preference")
			}
		})
	}
}

func TestStreamVideoQualityExpiresWithWatchAndRequestsOnlyTargetKeyframe(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	session := r.VideoWatchSession("sub")
	registerVideoSource(r, "pub", SlotScreen, "f", 101)
	registerVideoSource(r, "pub", SlotCam, "f", 102)
	feedback := &fakeRTCPWriter{}
	r.mu.Lock()
	r.rtcpWriters["pub"] = feedback
	r.mu.Unlock()
	if err := r.SetStreamVideoQuality("sub", "pub", SlotScreen, generation, session, "high"); err != nil {
		t.Fatal(err)
	}
	if feedback.pliCount() != 1 || feedback.pkts[0][0].(*rtcp.PictureLossIndication).MediaSSRC != 101 {
		t.Fatal("quality change requested wrong slot")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 2, session, true); err != nil {
		t.Fatal(err)
	}
	r.mu.RLock()
	got := r.streamLayerPrefLocked("sub", "pub", SlotScreen)
	r.mu.RUnlock()
	if got != "f" {
		t.Fatal("watch retry discarded preference")
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 3, session, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 4, session, true); err != nil {
		t.Fatal(err)
	}
	r.mu.RLock()
	got = r.streamLayerPrefLocked("sub", "pub", SlotScreen)
	r.mu.RUnlock()
	if got != "h" {
		t.Fatal("new watch inherited discarded preference")
	}
	r.DetachPeer("sub")
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if len(r.watches) != 0 {
		t.Fatal("teardown retained quality/watch state")
	}
}
