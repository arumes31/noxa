package webrtc

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

func updateVoiceLimits(t *testing.T, v *Voice, rate int, bounds VideoBounds) error {
	t.Helper()
	return v.SetVideoLimits(rate, bounds)
}

func runtimeVoice(t *testing.T) *Voice {
	t.Helper()
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	v := NewVoice(e, r, nil)
	v.renegClock = newManualRenegClock(time.Now())
	return v
}

func TestVoiceVideoLimitsInvalidAndClosedUpdatesPreserveState(t *testing.T) {
	v := runtimeVoice(t)
	bounds := VideoBounds{640, 360}
	if err := updateVoiceLimits(t, v, 800, bounds); err != nil {
		t.Fatal(err)
	}
	v.JoinChannel("publisher", 1)
	at := time.Now()
	if !v.router.allowVideoPacket("publisher", 100, at) {
		t.Fatal("initial budget unavailable")
	}
	api, policy := v.engine.api, v.router.videoPolicySnapshot()
	for _, tc := range []struct {
		name   string
		rate   int
		bounds VideoBounds
	}{
		{"negative bitrate", -1, VideoBounds{}},
		{"excess bitrate", 100_000_001, VideoBounds{1280, 720}},
		{"partial dimensions", 0, VideoBounds{640, 0}},
		{"excess dimensions", 0, VideoBounds{16384, 720}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := updateVoiceLimits(t, v, tc.rate, tc.bounds); err == nil {
				t.Fatal("invalid update accepted")
			}
			if v.engine.api != api || v.router.videoPolicySnapshot() != policy || v.router.allowVideoPacket("publisher", 1, at) {
				t.Fatal("invalid update changed codec, router policy or budget")
			}
		})
	}
	if err := updateVoiceLimits(t, v, 800, bounds); err != nil {
		t.Fatal(err)
	}
	if v.engine.api != api || v.router.videoPolicySnapshot() != policy || v.router.allowVideoPacket("publisher", 1, at) {
		t.Fatal("identical update changed state")
	}
	if err := v.engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := updateVoiceLimits(t, v, 0, VideoBounds{}); err == nil {
		t.Fatal("closed engine accepted coordinated update")
	}
	if v.engine.api != api || v.router.videoPolicySnapshot() != policy || v.router.allowVideoPacket("publisher", 1, at) {
		t.Fatal("closed engine update changed state")
	}
}

func TestVoiceVideoLimitsRebuildNegotiatesCurrentBounds(t *testing.T) {
	v := runtimeVoice(t)
	v.JoinChannel("publisher", 7)
	v.JoinChannel("listener", 7)
	fingerprint := v.engine.DTLSFingerprint()
	var previous *PeerConnectionWrapper
	for i, bounds := range []VideoBounds{{}, {1280, 720}, {640, 360}, {}} {
		if err := updateVoiceLimits(t, v, 800, bounds); err != nil {
			t.Fatal(err)
		}
		if previous != nil {
			if len(v.WhisperTargets("publisher")) != 1 {
				t.Fatal("updating limits changed whisper state")
			}
			select {
			case <-previous.Done():
				t.Fatal("updating limits prematurely closed peer")
			default:
			}
		}
		client := newClientPC(t)
		if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		establishVoiceSession(t, v, client, "publisher")
		answer := client.RemoteDescription().SDP
		if !strings.Contains(answer, "VP8/90000") || strings.Contains(answer, "AV1/90000") || !strings.Contains(strings.ToLower(answer), strings.ToLower(fingerprint)) {
			t.Fatalf("unexpected codec or identity after rebuild: %s", answer)
		}
		if strings.Contains(answer, "VP9/90000") != (bounds == (VideoBounds{})) {
			t.Fatal("rebuilt peer negotiated old codec policy")
		}
		if bounds != (VideoBounds{}) && !strings.Contains(answer, fmt.Sprintf("max-fs=%d", ((bounds.Width+15)/16)*((bounds.Height+15)/16))) {
			t.Fatal("rebuilt answer omitted current receiver bounds")
		}
		if previous != nil {
			select {
			case <-previous.Done():
			default:
				t.Fatal("rebuild retained old peer")
			}
		}
		previous = v.engine.PeerConnection("publisher")
		if v.PeerCount() != 1 || v.router.clientChan["publisher"] != 7 {
			t.Fatal("rebuild lost membership")
		}
		if i == 0 {
			v.SetWhisper("publisher", []string{"listener"}, nil, true)
		} else if got := v.WhisperTargets("publisher"); len(got) != 1 || got[0] != "listener" {
			t.Fatalf("rebuild lost whisper intent: %v", got)
		}
		// Dimension-only changes and peer rebuilds must not refill the budget.
		at := time.Unix(100, 0)
		if v.router.allowVideoPacket("publisher", 100, at) != (i == 0) {
			t.Fatal("dimension change or rebuild reset publisher budget")
		}
	}
	if err := v.ClosePeer("publisher"); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceVideoLimitsReofferOnExistingClient(t *testing.T) {
	v := runtimeVoice(t)
	v.JoinChannel("publisher", 7)
	client := newClientPC(t)
	if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	for _, bounds := range []VideoBounds{{}, {1280, 720}, {640, 360}, {}} {
		if err := v.SetVideoLimits(0, bounds); err != nil {
			t.Fatal(err)
		}
		establishVoiceSession(t, v, client, "publisher")
		answer := client.RemoteDescription().SDP
		if !strings.Contains(answer, "VP8/90000") {
			t.Fatal("existing client cannot renegotiate VP8")
		}
		if bounds != (VideoBounds{}) && !strings.Contains(answer, fmt.Sprintf("max-fs=%d", ((bounds.Width+15)/16)*((bounds.Height+15)/16))) {
			t.Fatal("existing client answer omitted current bounds")
		}
	}
}

func TestVoiceVideoLimitsConcurrentUpdatesAndPeerCreation(t *testing.T) {
	v := runtimeVoice(t)
	if err := updateVoiceLimits(t, v, 0, VideoBounds{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for step := range 12 {
				bounds := VideoBounds{640 + 16*worker, 360}
				if step%2 == 0 {
					bounds = VideoBounds{}
				}
				if err := updateVoiceLimits(t, v, worker*1000, bounds); err != nil {
					t.Error(err)
					return
				}
				id := fmt.Sprintf("%d-%d", worker, step)
				if _, err := v.engine.NewPeerConnection(id); err != nil {
					t.Error(err)
					return
				}
				if err := v.engine.ClosePeerConnection(id); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if v.engine.videoBounds != v.router.videoPolicySnapshot().bounds {
		t.Fatal("concurrent setters left engine and router with different bounds")
	}
}
