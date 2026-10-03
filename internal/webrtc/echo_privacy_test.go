package webrtc

import (
	"slices"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestEchoIsolatesAllMediaAndWhispers(t *testing.T) {
	r := NewRouter(nil)
	r.SetEchoChannel(99)
	r.JoinChannel(99, "a")
	r.JoinChannel(99, "b")
	r.JoinChannel(1, "outside")
	r.JoinChannel(1, "normal-peer")
	r.SetWhisper("a", []string{"b", "outside"}, []int64{1, 99}, true)
	r.SetWhisper("outside", []string{"a", "normal-peer"}, []int64{99}, true)
	if got := r.WhisperTargets("a"); len(got) != 0 {
		t.Fatalf("echo whisper recipients: %v", got)
	}
	if got := r.WhisperTargets("outside"); len(got) != 1 || got[0] != "normal-peer" {
		t.Fatalf("outside whisper recipients: %v", got)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, slot := range []string{SlotMic, SlotScreenAudio, SlotCam, SlotScreen} {
		if got := r.targetSubscribersLocked("a", slot); len(got) != 1 || got[0] != "a" {
			t.Errorf("echo %s recipients: %v", slot, got)
		}
		if got := r.targetSubscribersLocked("outside", slot); len(got) != 1 || got[0] != "normal-peer" {
			t.Errorf("normal %s recipients: %v", slot, got)
		}
		if r.mediaOutputLocked("a", "a", slot, false, nil).delivery.Whisper {
			t.Errorf("echo %s incorrectly marked as whisper", slot)
		}
	}
}

func TestEchoPrunesWhisperSubscriptionsAndRejectsVideoWatch(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	r := NewRouter(nil)
	r.SetEchoChannel(99)
	for _, id := range []string{"a", "b", "outside"} {
		attachFakePeer(t, e, r, id)
		r.JoinChannel(1, id)
	}
	r.SetWhisper("outside", []string{"a"}, nil, true)
	r.SetWhisper("a", []string{"outside"}, nil, true)
	r.JoinChannel(99, "a")
	r.JoinChannel(99, "b")
	for _, id := range []string{"a", "b", "outside"} {
		r.PrepareSubscriber(id)
		r.EnsurePublishers(id)
	}
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}, {"outside", "a"}, {"a", "outside"}} {
		if pubTrackFor(r, pair[0], pair[1]) != nil {
			t.Errorf("echo retained publisher pair %v", pair)
		}
	}
	if sent := r.ForwardRTP("a", SlotMic, makeAudioPacket(t, 1, -1)); sent != 1 {
		t.Fatalf("echo with active whisper sent=%d, want self only", sent)
	}
	generation, err := r.PublishVideo("a", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WatchVideo("b", "a", SlotScreen, generation, 1, r.VideoWatchSession("b"), true); err == nil {
		t.Fatal("echo participant subscribed to another participant's screen")
	}
	r.JoinChannel(1, "a")
	if got := r.WhisperTargets("a"); !slices.Equal(got, []string{"outside"}) {
		t.Fatalf("normal whisper did not resume: %v", got)
	}
}

func TestEchoRejectsQueuedAndRetransmittedMedia(t *testing.T) {
	for _, slot := range []string{SlotMic, SlotScreenAudio, SlotCam, SlotScreen} {
		for _, moving := range []string{"sender", "recipient"} {
			t.Run(slot+"/"+moving, func(t *testing.T) {
				r := NewRouter(nil)
				r.SetEchoChannel(99)
				r.JoinChannel(1, "sender")
				r.JoinChannel(1, "recipient")
				if slot == SlotCam || slot == SlotScreen || slot == SlotScreenAudio {
					watchedSlot := slot
					if slot == SlotScreenAudio {
						watchedSlot = SlotScreen
					}
					testVideoPublication(t, r, "sender", "recipient", watchedSlot)
				}
				r.mu.RLock()
				delivery := r.mediaOutputLocked("sender", "recipient", slot, false, nil).delivery
				r.mu.RUnlock()
				policy := r.videoPolicySnapshot()
				stream := &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}
				packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}, mediaTicket{router: r, delivery: delivery, videoRevision: policy.revision})
				if !ok {
					t.Fatal("ticket refused")
				}
				writes := 0
				terminal := func() {
					t.Helper()
					if _, err := stream.write(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { writes++; return 1, nil })); err != nil {
						t.Fatal(err)
					}
				}
				r.mu.Lock()
				terminal()
				r.mu.Unlock()
				if writes != 1 {
					t.Fatal("normal delivery dropped")
				}
				r.JoinChannel(99, moving)
				terminal()
				if err := r.mediaCommit(delivery, nil, policy.revision)(func() error { writes++; return nil }); err != nil {
					t.Fatal(err)
				}
				if writes != 1 {
					t.Fatal("queued media crossed echo boundary")
				}
			})
		}
	}
}

func TestEchoRejectsRecordersAndOldEchoSessions(t *testing.T) {
	r := NewRouter(nil)
	r.SetEchoChannel(99)
	r.JoinChannel(99, "sender")
	r.JoinChannel(99, "recorder")
	w := &fakeTrackWriter{}
	r.AddOutput("recorder", w)
	if sent := r.ForwardRTP("sender", SlotMic, makeAudioPacket(t, 1, -1)); sent != 0 || w.count() != 0 {
		t.Fatal("echo microphone reached channel recorder")
	}
	r.mu.RLock()
	delivery := r.mediaOutputLocked("sender", "sender", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	commit := r.mediaCommit(delivery, nil, 0)
	writes := 0
	if err := commit(func() error { writes++; return nil }); err != nil || writes != 1 {
		t.Fatalf("self echo dropped: writes=%d err=%v", writes, err)
	}
	r.JoinChannel(1, "sender")
	if err := commit(func() error { writes++; return nil }); err != nil {
		t.Fatal(err)
	}
	r.JoinChannel(99, "sender")
	if err := commit(func() error { writes++; return nil }); err != nil || writes != 1 {
		t.Fatalf("queued echo survived rejoin: writes=%d err=%v", writes, err)
	}
}

func TestEchoMoveWaitsForFinalWrite(t *testing.T) {
	r := NewRouter(nil)
	r.SetEchoChannel(99)
	r.JoinChannel(1, "sender")
	r.JoinChannel(1, "recipient")
	r.mu.RLock()
	delivery := r.mediaOutputLocked("sender", "recipient", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		completed <- r.mediaCommit(delivery, nil, 0)(func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("normal delivery did not reach final write")
	}
	changed := make(chan struct{})
	go func() { r.JoinChannel(99, "sender"); close(changed) }()
	select {
	case <-changed:
		t.Error("echo move completed during an in-flight final write")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("echo move did not finish after final write")
	}
}
