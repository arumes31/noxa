package webrtc

import (
	"testing"
	"time"
)

// A voice packet that meets a contended final-write gate (a control update
// holding the echo scope) is retried at the head of the voice queue and sent
// once the gate frees, and is dropped only when mediaAudioLifetime expires.
func TestVoicePacketRetriedWhileWriteGateBusy(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "released"
		if !release {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			r := NewRouter(nil)
			ticket := mediaTicket{router: r, delivery: MediaDelivery{SenderID: "a", RecipientID: "b", Slot: SlotMic}}
			p, _, packet, writes := terminalDiagnosticFixture(t, ticket)
			p.streams[11].audio = true
			p.audioWake = make(chan struct{}, 1)
			p.stop = make(chan struct{})
			done := make(chan struct{})
			go p.runAudio(p.audioWake, done)
			defer func() { close(p.stop); <-done }()

			r.echoScopeMu.Lock()
			locked := true
			defer func() {
				if locked {
					r.echoScopeMu.Unlock()
				}
			}()
			if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
				t.Fatal(err)
			}
			// The worker pops the packet for each attempt and puts it back
			// when the gate is busy, so a single snapshot can land in the gap;
			// poll until the packet is seen back at the queue head.
			time.Sleep(20 * time.Millisecond)
			var queued int
			seen := time.Now().Add(50 * time.Millisecond)
			for {
				p.mu.Lock()
				queued = p.audioCount
				p.mu.Unlock()
				if writes.Load() != 0 {
					t.Fatalf("busy voice packet was written through a held gate (writes=%d)", writes.Load())
				}
				if queued == 1 {
					break
				}
				if time.Now().After(seen) {
					t.Fatalf("busy voice packet: writes=%d queued=%d, want 0 and 1", writes.Load(), queued)
				}
				time.Sleep(100 * time.Microsecond)
			}
			if !release {
				time.Sleep(mediaAudioLifetime)
			}
			r.echoScopeMu.Unlock()
			locked = false
			deadline := time.Now().Add(time.Second)
			for {
				p.mu.Lock()
				queued = p.audioCount
				p.mu.Unlock()
				if (release && writes.Load() == 1) || (!release && queued == 0) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("writes=%d queued=%d", writes.Load(), queued)
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(10 * time.Millisecond)
			if want := map[bool]int64{true: 1, false: 0}[release]; writes.Load() != want {
				t.Fatalf("writes=%d, want %d", writes.Load(), want)
			}
		})
	}
}
