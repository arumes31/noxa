package webrtc

import (
	"fmt"
	"testing"
)

func hevcSelectionRouter() *Router {
	r := NewRouter(nil)
	for _, id := range []string{"pub", "sub", "legacy"} {
		r.JoinChannel(1, id)
	}
	return r
}

func assertHEVCSelection(t *testing.T, r *Router, codec string, level uint8) {
	t.Helper()
	for _, p := range r.VideoPublications("pub") {
		if p.PublisherID == "pub" && p.Slot == SlotScreen {
			if p.Codec != codec || p.HEVCLevelID != level {
				t.Fatalf("selection = %s/%d, want %s/%d", p.Codec, p.HEVCLevelID, codec, level)
			}
			return
		}
	}
	t.Fatal("publication missing")
}

func TestHEVCSelectionLevelIntersection(t *testing.T) {
	levels := []uint8{30, 60, 63, 90, 93, 120, 123, 150, 153, 156, 180, 183, 186}
	codecs := []string{"h265", "h264", "vp8"}
	for _, publisher := range levels {
		for _, receiver := range levels {
			t.Run(fmt.Sprintf("publisher%d_receiver%d", publisher, receiver), func(t *testing.T) {
				r := hevcSelectionRouter()
				generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", codecs, publisher)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true, codecs, receiver); err != nil {
					t.Fatal(err)
				}
				if receiver >= publisher {
					assertHEVCSelection(t, r, "h265", publisher)
				} else {
					assertHEVCSelection(t, r, "h264", 0)
				}
			})
		}
	}
}

func TestHEVCSelectionRejectsInvalidControlFormats(t *testing.T) {
	cases := []struct {
		name   string
		codecs []string
		level  uint8
	}{
		{"missing level", []string{"h265", "vp8"}, 0},
		{"invalid low", []string{"h265", "vp8"}, 1},
		{"invalid intermediate", []string{"h265", "vp8"}, 31},
		{"invalid high", []string{"h265", "vp8"}, 255},
		{"level without hevc", []string{"h264", "vp8"}, 123},
		{"legacy with level", nil, 123},
		{"missing compatibility floor", []string{"h265", "h264"}, 123},
		{"duplicate", []string{"h265", "h265", "vp8"}, 123},
		{"unknown", []string{"h265", "vp9", "vp8"}, 123},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := hevcSelectionRouter()
			if _, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", tc.codecs, tc.level); err == nil {
				t.Fatal("invalid publisher accepted")
			}
			if len(r.VideoPublications("pub")) != 0 {
				t.Fatal("rejected publisher mutated catalog")
			}
			generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"h265", "h264", "vp8"}, 123)
			if err != nil {
				t.Fatal(err)
			}
			session := r.VideoWatchSession("sub")
			epoch := r.watchEpoch
			if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, session, true, tc.codecs, tc.level); err == nil {
				t.Fatal("invalid receiver accepted")
			}
			if r.watchEpoch != epoch || len(r.watches) != 0 {
				t.Fatal("rejected receiver mutated watch state")
			}
			assertHEVCSelection(t, r, "h265", 123)
		})
	}
	r := hevcSelectionRouter()
	if _, err := r.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"h265", "vp8"}); err == nil {
		t.Fatal("legacy API admitted HEVC without level")
	}
	generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"h265", "vp8"}, 123)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WatchVideoWithCodecs("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true, []string{"h265", "vp8"}); err == nil {
		t.Fatal("legacy watch API admitted HEVC without level")
	}
}

func TestHEVCSelectionWatchRevisionFallbackAndPrivacy(t *testing.T) {
	r := hevcSelectionRouter()
	codecs := []string{"h265", "h264", "vp8"}
	generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", codecs, 123)
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("sub")
	watch := func(revision uint64, level uint8) {
		t.Helper()
		if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, revision, session, true, codecs, level); err != nil {
			t.Fatal(err)
		}
	}
	watch(1, 120)
	assertHEVCSelection(t, r, "h264", 0)
	key := watchKey{"sub", "pub", SlotScreen}
	old := r.watches[key]
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, session, true, codecs, 123); err == nil {
		t.Fatal("exact retry changed receiver ceiling")
	}
	if r.watches[key] != old {
		t.Fatal("rejected retry changed watch")
	}
	watch(2, 123)
	assertHEVCSelection(t, r, "h265", 123)
	if r.watches[key].epoch == old.epoch {
		t.Fatal("upgrade reused queued-packet epoch")
	}
	upgraded := r.watches[key]
	watch(3, 120)
	assertHEVCSelection(t, r, "h264", 0)
	if r.watches[key].epoch == upgraded.epoch {
		t.Fatal("downgrade reused queued-packet epoch")
	}
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 2, session, true, codecs, 186); err == nil {
		t.Fatal("stale upgrade accepted")
	}
	watch(4, 186)
	assertHEVCSelection(t, r, "h265", 123)
	legacySession := r.VideoWatchSession("legacy")
	if _, err := r.WatchVideo("legacy", "pub", SlotScreen, generation, 1, legacySession, true); err != nil {
		t.Fatal(err)
	}
	assertHEVCSelection(t, r, "vp8", 0)
	if _, err := r.WatchVideo("legacy", "pub", SlotScreen, generation, 2, legacySession, false); err != nil {
		t.Fatal(err)
	}
	assertHEVCSelection(t, r, "h265", 123)
	for _, role := range []string{"sub", "legacy"} {
		for _, p := range r.VideoPublications(role) {
			if p.Codec != "" || p.HEVCLevelID != 0 || p.ViewerCount != nil || p.UploadActive != nil {
				t.Fatalf("private publication decision exposed to %s: %+v", role, p)
			}
		}
	}
	watch(5, 120)
	assertHEVCSelection(t, r, "h264", 0)
	r.LeaveChannel(1, "sub")
	assertHEVCSelection(t, r, "h265", 123)
	r.JoinChannel(1, "sub")
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 6, session, true, codecs, 120); err == nil {
		t.Fatal("retired session accepted")
	}
	assertHEVCSelection(t, r, "h265", 123)
}

func TestHEVCSelectionAugmentationAndPublicationReplacement(t *testing.T) {
	r := hevcSelectionRouter()
	generation, err := r.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"vp8"})
	if err != nil {
		t.Fatal(err)
	}
	codecs := []string{"h265", "h264", "vp8"}
	session := r.VideoWatchSession("sub")
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, session, true, codecs, 186); err != nil {
		t.Fatal(err)
	}
	key := watchKey{"sub", "pub", SlotScreen}
	oldWatch, oldEpoch := r.watches[key], r.watchEpoch
	for _, update := range []struct {
		codecs []string
		level  uint8
	}{{[]string{"h264", "vp8"}, 0}, {codecs, 123}, {codecs, 123}} {
		got, err := r.PublishVideoWithFormats("pub", SlotScreen, generation, true, "source", update.codecs, update.level)
		if err != nil || got != generation {
			t.Fatalf("augmentation generation=%d error=%v", got, err)
		}
		if r.watches[key] != oldWatch || r.watchEpoch != oldEpoch {
			t.Fatal("augmentation replaced existing source/watch epoch")
		}
	}
	assertHEVCSelection(t, r, "h265", 123)
	for _, update := range []struct {
		codecs     []string
		level      uint8
		generation uint64
	}{
		{codecs, 120, generation}, {codecs, 150, generation}, {[]string{"h265", "vp8"}, 123, generation}, {[]string{"h264", "vp8"}, 0, generation}, {codecs, 123, generation + 1},
	} {
		if _, err := r.PublishVideoWithFormats("pub", SlotScreen, update.generation, true, "source", update.codecs, update.level); err == nil {
			t.Fatal("level/capability/scope replacement accepted")
		}
		assertHEVCSelection(t, r, "h265", 123)
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	pkey := publicationKey{"pub", SlotScreen}
	if _, ok := r.publicationHEVCLevels[pkey]; ok {
		t.Fatal("stopped publication retained HEVC level")
	}
	if len(r.watches) != 0 {
		t.Fatal("stopped publication retained watches")
	}
	replacement, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil || replacement == generation {
		t.Fatalf("replacement=%d error=%v", replacement, err)
	}
	assertHEVCSelection(t, r, "vp8", 0)
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 2, session, true, codecs, 186); err == nil {
		t.Fatal("stale generation accepted")
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err == nil {
		t.Fatal("stale stop removed replacement")
	}
	assertHEVCSelection(t, r, "vp8", 0)
}

func TestHEVCSelectionPolicyAndPublisherRemoval(t *testing.T) {
	for _, policy := range []string{"bounds", "recording", "publisher leaves"} {
		t.Run(policy, func(t *testing.T) {
			r := hevcSelectionRouter()
			if _, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"h265", "h264", "vp8"}, 123); err != nil {
				t.Fatal(err)
			}
			assertHEVCSelection(t, r, "h265", 123)
			switch policy {
			case "bounds":
				if err := r.SetVideoLimits(0, VideoBounds{1280, 720}); err != nil {
					t.Fatal(err)
				}
				assertHEVCSelection(t, r, "vp8", 0)
				if err := r.SetVideoLimits(0, VideoBounds{}); err != nil {
					t.Fatal(err)
				}
				assertHEVCSelection(t, r, "h265", 123)
			case "recording":
				r.JoinChannel(1, "recorder")
				r.SetTrackSlots("pub", map[string]string{"share": SlotScreen})
				r.addVideoOutput("recorder", &scopedCapture{})
				assertHEVCSelection(t, r, "vp8", 0)
			case "publisher leaves":
				r.LeaveChannel(1, "pub")
				if len(r.VideoPublications("sub")) != 0 || len(r.publicationHEVCLevels) != 0 {
					t.Fatal("departed publication/level retained")
				}
				r.JoinChannel(1, "pub")
				if _, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", []string{"h265", "vp8"}, 90); err != nil {
					t.Fatal(err)
				}
				assertHEVCSelection(t, r, "h265", 90)
			}
		})
	}
}

func TestHEVCSelectionVersionedPublisherCapabilityReplacement(t *testing.T) {
	r := hevcSelectionRouter()
	codecs := []string{"h265", "h264", "vp8"}
	generation, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", codecs, 123, 1)
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("sub")
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 1, session, true, codecs, 123); err != nil {
		t.Fatal(err)
	}
	key := watchKey{"sub", "pub", SlotScreen}
	watch, epoch := r.watches[key], r.watchEpoch
	assertRevision := func(want uint64) {
		t.Helper()
		for _, p := range r.VideoPublications("pub") {
			if p.PublisherID == "pub" && p.Slot == SlotScreen {
				if p.CodecRevision != want {
					t.Fatalf("revision=%d want=%d", p.CodecRevision, want)
				}
				return
			}
		}
		t.Fatal("publication missing")
	}
	update := func(revision uint64, formats []string, level uint8) {
		t.Helper()
		got, err := r.PublishVideoWithFormats("pub", SlotScreen, generation, true, "source", formats, level, revision)
		if err != nil || got != generation {
			t.Fatalf("revision%d generation=%d err=%v", revision, got, err)
		}
		if r.watches[key] != watch || r.watchEpoch != epoch {
			t.Fatal("capability change replaced source/watch lifecycle")
		}
		assertRevision(revision)
	}
	assertRevision(1)
	update(2, []string{"h264", "vp8"}, 0)
	assertHEVCSelection(t, r, "h264", 0)
	update(2, []string{"h264", "vp8"}, 0)
	for _, tc := range []struct {
		revision uint64
		formats  []string
		level    uint8
		gen      uint64
		mode     string
	}{
		{1, codecs, 123, generation, "source"},
		{2, codecs, 123, generation, "source"},
		{3, codecs, 123, generation + 1, "source"},
		{3, codecs, 123, 0, "source"},
		{3, codecs, 123, generation, ""},
		{3, codecs, 123, generation, "invalid"},
		{0, codecs, 123, generation, "source"},
	} {
		if _, err := r.PublishVideoWithFormats("pub", SlotScreen, tc.gen, true, tc.mode, tc.formats, tc.level, tc.revision); err == nil {
			t.Fatalf("stale/changed retry/wrong scope accepted: %+v", tc)
		}
		assertHEVCSelection(t, r, "h264", 0)
		assertRevision(2)
	}
	if _, err := r.PublishVideoWithFormats("pub", SlotScreen, generation, true, "source", codecs, 123); err == nil {
		t.Fatal("legacy augmentation overwrote versioned publication")
	}
	update(3, codecs, 150)
	assertHEVCSelection(t, r, "h264", 0) // Receiver ceiling still 123.
	if _, err := r.WatchVideoWithFormats("sub", "pub", SlotScreen, generation, 2, session, true, codecs, 150); err != nil {
		t.Fatal(err)
	}
	assertHEVCSelection(t, r, "h265", 150)
	watch, epoch = r.watches[key], r.watchEpoch
	update(4, []string{"vp8"}, 0)
	assertHEVCSelection(t, r, "vp8", 0)
	update(5, codecs, 123)
	assertHEVCSelection(t, r, "h265", 123)
	for _, p := range r.VideoPublications("sub") {
		if p.CodecRevision != 0 || p.HEVCLevelID != 0 || p.Codec != "" {
			t.Fatal("publisher capability revision leaked to receiver")
		}
	}
	if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	next, err := r.PublishVideoWithFormats("pub", SlotScreen, 0, true, "source", codecs, 90, 1)
	if err != nil || next == generation {
		t.Fatalf("fresh revision1 failed: generation=%d err=%v", next, err)
	}
	assertRevision(1)
	assertHEVCSelection(t, r, "h265", 90)
	if _, err := r.PublishVideoWithFormats("pub", SlotScreen, generation, true, "source", codecs, 123, 6); err == nil {
		t.Fatal("old high revision overwrote replacement publication")
	}
	assertRevision(1)
	assertHEVCSelection(t, r, "h265", 90)
}
