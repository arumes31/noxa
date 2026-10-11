package webrtc

import (
	"reflect"
	"testing"
	"time"
)

func TestPublicationCodecRefreshPreservesSourceLifetime(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "sub")
	generation, err := r.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"vp8"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WatchVideoWithCodecs("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true, []string{"vp8", "h264"}); err != nil {
		t.Fatal(err)
	}
	key := publicationKey{"pub", SlotScreen}
	wkey := watchKey{"sub", "pub", SlotScreen}
	iKey := videoIngressKey{"pub", SlotScreen, ""}
	input := &videoIngressDiagnostic{ssrc: 123, started: time.Now(), stage: videoStageCounter{frames: 42}}
	r.videoIngress = map[videoIngressKey]*videoIngressDiagnostic{iKey: input}
	preview := videoPreview{jpeg: []byte{1, 2, 3}, captured: time.Now()}
	r.previews = map[publicationKey]videoPreview{key: preview}
	watch, epoch := r.watches[wkey], r.watchEpoch
	for range 2 {
		got, err := r.PublishVideoWithCodecs("pub", SlotScreen, generation, true, "source", []string{"vp8", "h264"})
		if err != nil || got != generation {
			t.Fatalf("refresh: generation=%d err=%v", got, err)
		}
		if r.watches[wkey] != watch || r.watchEpoch != epoch || !reflect.DeepEqual(r.previews[key], preview) || r.videoIngress[iKey] != input || input.stage.frames != 42 {
			t.Fatal("codec refresh replaced publication, watch, preview or ingress lifetime")
		}
	}
	if got := selectedPublicationCodec(t, r, "pub"); got != "h264" {
		t.Fatalf("selected codec: %s", got)
	}
}

func TestPublicationCodecRefreshRejectsWrongScope(t *testing.T) {
	for _, scenario := range []string{"zero generation", "stale generation", "mode omitted", "mode changed", "legacy publication", "missing source", "no channel", "capability removal"} {
		t.Run(scenario, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			mode, codecs := "source", []string{"vp8"}
			if scenario == "legacy publication" {
				mode = ""
			}
			if scenario == "capability removal" {
				codecs = []string{"vp8", "h264"}
			}
			generation, err := r.PublishVideoWithCodecs("pub", SlotScreen, 0, true, mode, codecs)
			if err != nil {
				t.Fatal(err)
			}
			requestGeneration, requestMode, requestCodecs := generation, "source", []string{"vp8", "h264"}
			switch scenario {
			case "zero generation":
				requestGeneration = 0
			case "stale generation":
				requestGeneration++
			case "mode omitted":
				requestMode = ""
			case "mode changed":
				// An already refreshed source cannot be converted into legacy mode.
				requestMode = "invalid"
			case "missing source":
				if _, err := r.PublishVideo("pub", SlotScreen, generation, false); err != nil {
					t.Fatal(err)
				}
			case "no channel":
				r.LeaveChannel(1, "pub")
			case "capability removal":
				requestCodecs = []string{"vp8"}
			}
			if _, err := r.PublishVideoWithCodecs("pub", SlotScreen, requestGeneration, true, requestMode, requestCodecs); err == nil {
				t.Fatal("out-of-scope refresh accepted")
			}
		})
	}
}

func TestPublicationCodecRefreshKeepsReceiverAndPolicyIntersection(t *testing.T) {
	for _, constraint := range []string{"legacy viewer", "dimension bounds", "recording"} {
		t.Run(constraint, func(t *testing.T) {
			r := NewRouter(nil)
			r.JoinChannel(1, "pub")
			generation, err := r.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"vp8"})
			if err != nil {
				t.Fatal(err)
			}
			switch constraint {
			case "legacy viewer":
				r.JoinChannel(1, "sub")
				if _, err := r.WatchVideo("sub", "pub", SlotScreen, generation, 1, r.VideoWatchSession("sub"), true); err != nil {
					t.Fatal(err)
				}
			case "dimension bounds":
				if err := r.SetVideoLimits(0, VideoBounds{1280, 720}); err != nil {
					t.Fatal(err)
				}
			case "recording":
				r.JoinChannel(1, "recorder")
				r.SetTrackSlots("pub", map[string]string{"share": SlotScreen})
				r.addVideoOutput("recorder", &scopedCapture{})
			}
			if _, err := r.PublishVideoWithCodecs("pub", SlotScreen, generation, true, "source", []string{"vp8", "h264"}); err != nil {
				t.Fatal(err)
			}
			if got := selectedPublicationCodec(t, r, "pub"); got != "vp8" {
				t.Fatalf("refresh bypassed %s: %s", constraint, got)
			}
		})
	}
}
