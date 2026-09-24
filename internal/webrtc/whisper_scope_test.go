package webrtc

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestWhisperOnlyChangesMicrophoneRouting(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "sender")
	r.JoinChannel(1, "channel-peer")
	r.JoinChannel(2, "whisper-peer")
	r.SetWhisper("sender", []string{"whisper-peer"}, nil, true)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, slot := range []string{SlotMic, SlotScreenAudio, SlotCam, SlotScreen} {
		got := r.targetSubscribersLocked("sender", slot)
		want := "channel-peer"
		if slot == SlotMic {
			want = "whisper-peer"
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("slot %s targets=%v want %s", slot, got, want)
		}
		output := r.mediaOutputLocked("sender", want, slot, false, nil)
		if output.delivery.Whisper != (slot == SlotMic) {
			t.Fatalf("slot %s mislabeled whisper", slot)
		}
	}
}

func TestWhisperQueuedDeliveryCannotCrossRecipientChanges(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "sender")
	r.JoinChannel(2, "a")
	r.JoinChannel(2, "b")
	r.SetWhisper("sender", []string{"a"}, nil, true)
	r.mu.RLock()
	delivery := r.mediaOutputLocked("sender", "a", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	commit := r.mediaCommit(delivery, nil, 0)
	writes := 0
	if err := commit(func() error { writes++; return nil }); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal("current whisper was dropped")
	}
	r.SetWhisper("sender", []string{"b"}, nil, true)
	if err := commit(func() error { writes++; return nil }); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal("queued whisper reached a removed recipient")
	}
	r.SetWhisper("sender", []string{"a"}, nil, true)
	if err := commit(func() error { writes++; return nil }); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal("old whisper revived when recipient was re-added")
	}
}

func TestWhisperTerminalDeliveryRejectsOldChannelMembership(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "sender")
	r.JoinChannel(2, "recipient")
	r.SetWhisper("sender", nil, []int64{2}, true)
	r.mu.RLock()
	delivery := r.mediaOutputLocked("sender", "recipient", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	stream := &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}
	packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}}, mediaTicket{router: r, delivery: delivery})
	if !ok {
		t.Fatal("ticket refused")
	}
	writes := 0
	terminal := func() {
		t.Helper()
		if _, err := stream.write(&packet.Header, packet.Payload, nil, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			writes++
			return 1, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	// The terminal path must not acquire Router.mu, even for retransmissions.
	r.mu.Lock()
	terminal()
	r.mu.Unlock()
	if writes != 1 {
		t.Fatal("current whisper was dropped")
	}
	r.LeaveChannel(2, "recipient")
	terminal()
	r.JoinChannel(2, "recipient")
	terminal()
	if writes != 1 {
		t.Fatal("cached packet survived whisper channel membership changes")
	}
}

func TestWhisperRecordingCommitRevokedOnActivation(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "sender")
	r.JoinChannel(2, "recipient")
	capture := func() MediaCommit {
		r.mu.RLock()
		delivery := r.mediaOutputLocked("sender", "recorder", SlotMic, true, nil).delivery
		r.mu.RUnlock()
		return r.mediaCommit(delivery, nil, 0)
	}
	queued := capture()
	r.SetWhisper("sender", []string{"recipient"}, nil, true)
	for _, commit := range []MediaCommit{queued, capture()} {
		if err := commit(func() error { t.Error("whisper entered channel recording"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWhisperChangeWaitsForFinalWrite(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "sender")
	r.JoinChannel(2, "recipient")
	r.SetWhisper("sender", []string{"recipient"}, nil, true)
	r.mu.RLock()
	delivery := r.mediaOutputLocked("sender", "recipient", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		completed <- r.mediaCommit(delivery, nil, 0)(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	changed := make(chan struct{})
	go func() { r.SetWhisper("sender", nil, nil, false); close(changed) }()
	select {
	case <-changed:
		t.Error("recipient change completed during an in-flight final write")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("recipient change did not complete after write")
	}
}
