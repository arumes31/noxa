package webrtc

import "testing"

func selectedPublicationCodec(t *testing.T, router *Router, publisher string) string {
	t.Helper()
	for _, stream := range router.VideoPublications(publisher) {
		if stream.PublisherID == publisher && stream.Slot == SlotScreen {
			return stream.Codec
		}
	}
	t.Fatal("missing publication")
	return ""
}

func TestPublicationCodecIntersectionAndLegacyFallback(t *testing.T) {
	router := NewRouter(nil)
	for _, id := range []string{"pub", "modern", "legacy"} {
		router.JoinChannel(1, id)
	}
	codecs := []string{"h264", "vp8"}
	generation, err := router.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", codecs)
	if err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "h264" {
		t.Fatal("hardware codec not preferred")
	}
	modern := router.VideoWatchSession("modern")
	legacy := router.VideoWatchSession("legacy")
	if _, err := router.WatchVideoWithCodecs("modern", "pub", SlotScreen, generation, 1, modern, true, codecs); err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "h264" {
		t.Fatal("compatible viewer changed codec")
	}
	if _, err := router.WatchVideo("legacy", "pub", SlotScreen, generation, 1, legacy, true); err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "vp8" {
		t.Fatal("legacy viewer did not trigger shared fallback")
	}
	if _, err := router.WatchVideoWithCodecs("legacy", "pub", SlotScreen, generation, 1, legacy, true, codecs); err == nil {
		t.Fatal("exact retry changed capabilities")
	}
	if selectedPublicationCodec(t, router, "pub") != "vp8" {
		t.Fatal("stale capabilities changed selection")
	}
	for _, stream := range router.VideoPublications("modern") {
		if stream.Codec != "" {
			t.Fatal("receiver sees publication capabilities")
		}
	}
	if _, err := router.WatchVideo("legacy", "pub", SlotScreen, generation, 2, legacy, false); err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "h264" {
		t.Fatal("stopped viewer still restricts codec")
	}
	if err := router.SetVideoLimits(0, VideoBounds{1280, 720}); err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "vp8" {
		t.Fatal("H264 bypassed dimension inspection policy")
	}
}

func TestPublicationCodecRejectsUnknownFormatsAndReplacement(t *testing.T) {
	router := NewRouter(nil)
	router.JoinChannel(1, "pub")
	for _, codecs := range [][]string{{"h265", "vp8"}, {"h264"}, {"vp8", "vp8"}, {"h264", "vp8", "vp9"}} {
		if _, err := router.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", codecs); err == nil {
			t.Fatalf("invalid capabilities accepted: %v", codecs)
		}
	}
	generation, err := router.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"h264", "vp8"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.PublishVideo("pub", SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	if _, err := router.PublishVideo("pub", SlotScreen, 0, true); err != nil {
		t.Fatal(err)
	}
	if selectedPublicationCodec(t, router, "pub") != "vp8" {
		t.Fatal("old hardware capability leaked into replacement")
	}
}
