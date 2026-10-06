package webrtc

import (
	"sync"
	"testing"
)

func assertVideoAudience(t *testing.T, r *Router, publisher, slot, mode string, viewers int, active bool) {
	t.Helper()
	for _, publication := range r.VideoPublications(publisher) {
		if publication.PublisherID != publisher || publication.Slot != slot {
			continue
		}
		if publication.UploadActive == nil || *publication.UploadActive != active || publication.ViewerCount == nil ||
			*publication.ViewerCount != viewers || publication.QualityMode != mode {
			t.Fatalf("%s/%s audience %+v, want mode=%q viewers=%d active=%v", publisher, slot, publication, mode, viewers, active)
		}
		return
	}
	t.Fatalf("missing publication %s/%s", publisher, slot)
}

func TestVideoSourceAudienceStartsBeforeRTPAndExpiresWithAuthorizedWatch(t *testing.T) {
	r := NewRouter(nil)
	for _, id := range []string{"pub", "a", "b"} {
		r.JoinChannel(1, id)
	}
	generation, err := r.PublishVideoWithMode("pub", SlotScreen, 0, true, "source")
	if err != nil {
		t.Fatal(err)
	}
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, false)
	for _, id := range []string{"a", "b"} {
		if _, err := r.WatchVideo(id, "pub", SlotScreen, generation, 1, r.VideoWatchSession(id), true); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing has published RTP or registered an SSRC; intent must still wake
	// the dormant publisher. Observed frame rate cannot determine this state.
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 2, true)
	for _, stream := range r.VideoPublications("a") {
		if stream.UploadActive != nil || stream.ViewerCount != nil || stream.QualityMode != "source" {
			t.Fatal("publisher audience disclosed or source mode missing from viewer catalog")
		}
	}
	if _, err := r.WatchVideo("a", "pub", SlotScreen, generation, 2, r.VideoWatchSession("a"), false); err != nil {
		t.Fatal(err)
	}
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 1, true)
	r.SetPublisherGuard(func(access PublisherAccess) bool { return access.SubscriberID != "b" })
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, false)
	r.SetPublisherGuard(nil)
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, false)
	if _, err := r.WatchVideo("b", "pub", SlotScreen, generation, 2, r.VideoWatchSession("b"), true); err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(2, "b")
	assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, false)
}

func TestVideoSourceModeAndAudienceArePublicationScoped(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	if _, err := r.PublishVideoWithMode("pub", SlotCam, 0, true, "unknown"); err == nil {
		t.Fatal("invalid mode created publication")
	}
	generation, err := r.PublishVideoWithMode("pub", SlotCam, 0, true, "source")
	if err != nil {
		t.Fatal(err)
	}
	testVideoPublication(t, r, "pub", "viewer", SlotCam)
	session := r.VideoWatchSession("viewer")
	for _, quality := range []string{"low", "mid"} {
		if err := r.SetStreamVideoQuality("viewer", "pub", SlotCam, generation, session, quality); err == nil {
			t.Fatalf("source mode accepted receiver override %s", quality)
		}
	}
	for _, quality := range []string{"high", "default"} {
		if err := r.SetStreamVideoQuality("viewer", "pub", SlotCam, generation, session, quality); err != nil {
			t.Fatal(err)
		}
	}
	registerVideoSource(r, "pub", SlotCam, "", 111)
	if err := r.SetVideoQuality("viewer", "low"); err != nil {
		t.Fatal(err)
	}
	r.mu.RLock()
	accept := r.acceptLayerLocked("viewer", "pub", SlotCam, "")
	r.mu.RUnlock()
	if !accept {
		t.Fatal("legacy receiver preference filtered the sole source encoding")
	}
	assertVideoAudience(t, r, "pub", SlotCam, "source", 1, true)
	r.DetachPeerKeepChannel("viewer")
	assertVideoAudience(t, r, "pub", SlotCam, "source", 0, false)
	if _, err := r.WatchVideo("viewer", "pub", SlotCam, generation, 2, session, true); err == nil {
		t.Fatal("stale receiver reactivated upload")
	}
	r.RevokeVideo("pub", SlotCam)
	if _, err := r.PublishVideo("pub", SlotCam, 0, true); err != nil {
		t.Fatal(err)
	}
	assertVideoAudience(t, r, "pub", SlotCam, "", 0, false)
	r.DetachPeerKeepChannel("pub")
	if len(r.publicationModes) != 0 {
		t.Fatal("publication mode survived teardown")
	}
}

func TestVideoAudienceIncludesRecordingConsumerWithoutViewerIdentity(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "scoped"}[scoped], func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			r.JoinChannel(1, "recorder")
			for _, slot := range []string{SlotCam, SlotScreen} {
				if _, err := r.PublishVideoWithMode("pub", slot, 0, true, "source"); err != nil {
					t.Fatal(err)
				}
			}
			r.SetTrackSlots("pub", map[string]string{"camera": SlotCam, "share": SlotScreen})
			if scoped {
				r.addVideoOutput("recorder", &scopedCapture{})
			} else {
				r.addVideoOutput("recorder", &fakeTrackWriter{})
			}
			assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, true)
			assertVideoAudience(t, r, "pub", SlotCam, "source", 0, scoped)
			r.DetachPeer("recorder")
			assertVideoAudience(t, r, "pub", SlotScreen, "source", 0, false)
		})
	}
}

func TestVideoAudienceConcurrentCatalogAndWatchChanges(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation := testVideoPublication(t, r, "pub", "sub", SlotScreen)
	session := r.VideoWatchSession("sub")
	var workers sync.WaitGroup
	workers.Go(func() {
		for revision := uint64(2); revision < 200; revision++ {
			_, _ = r.WatchVideo("sub", "pub", SlotScreen, generation, revision, session, revision%2 == 0)
		}
	})
	workers.Go(func() {
		for range 200 {
			for _, publication := range r.VideoPublications("pub") {
				if publication.UploadActive == nil || publication.ViewerCount == nil || *publication.UploadActive != (*publication.ViewerCount > 0) {
					t.Error("inconsistent concurrent audience snapshot")
					return
				}
			}
		}
	})
	workers.Wait()
}
