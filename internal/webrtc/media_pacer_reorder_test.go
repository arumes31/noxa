package webrtc

import (
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestMediaPacerDeliversLateReorderBacklogThroughFinalAuthority(t *testing.T) {
	for _, bitrate := range []int{8_000_000, 50_000_000} {
		t.Run(fmt.Sprintf("%dMbps", bitrate/1_000_000), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				interval := time.Second * 1200 * 8 / time.Duration(bitrate)
				count := int(2400 * time.Millisecond / interval)
				expectedWrites := int32(count + 2)
				registry := &mediaEgressRegistry{streams: make(map[string]*mediaEgressStream)}
				stream := &mediaEgressStream{active: true, registry: registry}
				registry.streams["video"] = stream
				p := newMediaPacer()
				p.egress = registry
				p.SetTargetBitrate(bitrate)
				defer func() { _ = p.Close(); stream.stop() }()
				var writes, checks, audioWrites atomic.Int32
				var badOrder atomic.Bool
				p.AddStream(1, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
					expected := writes.Add(1) - 1
					if h.SequenceNumber != uint16(expected) || len(h.CSRC) != 0 {
						badOrder.Store(true)
					}
					return h.MarshalSize() + len(payload), nil
				}))
				audioStream := &mediaEgressStream{active: true, registry: registry}
				registry.streams["audio"] = audioStream
				defer audioStream.stop()
				p.AddStream(2, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
					audioWrites.Add(1)
					return h.MarshalSize() + len(payload), nil
				}))
				p.mu.Lock()
				p.streams[1].bindingID = "video"
				p.streams[2].bindingID, p.streams[2].audio = "audio", true
				p.mu.Unlock()
				ticket := mediaTicket{delivery: MediaDelivery{Slot: SlotScreen}, guard: func(_ MediaDelivery, write func() error) error {
					checks.Add(1)
					return write()
				}}
				emit := func(packet *rtp.Packet, _ string) {
					prepared, ok := stream.prepare(packet, ticket)
					if !ok {
						t.Fatal("video ticket refused")
					}
					if _, err := p.Write(&prepared.Header, prepared.Payload, nil); err != nil {
						t.Fatal(err)
					}
				}
				packet := func(sequence uint16) *rtp.Packet {
					return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1, SequenceNumber: sequence}, Payload: make([]byte, 1188)}
				}
				var reorder videoReorder
				reorder.push(packet(0), "video/VP8", time.Now(), emit)
				// Lose sequence 1 while 2.4 seconds of 1200-byte RTP keeps arriving.
				for sequence := uint16(2); int(sequence) <= count+1; sequence++ {
					time.Sleep(interval)
					reorder.push(packet(sequence), "video/VP8", time.Now(), emit)
				}
				synctest.Wait()
				if writes.Load() != 1 {
					t.Fatal("unrepaired gap escaped")
				}
				repairedAt := time.Now()
				reorder.push(packet(1), "video/VP8", repairedAt, emit)
				if len(reorder.pending) != 0 {
					t.Fatal("repaired gap was not immediately released")
				}
				audio, ok := audioStream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 2}, Payload: []byte{1}}, mediaTicket{})
				if !ok {
					t.Fatal("audio ticket refused")
				}
				if _, err := p.Write(&audio.Header, audio.Payload, nil); err != nil {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
				synctest.Wait()
				if audioWrites.Load() != 1 || writes.Load() >= expectedWrites {
					t.Fatal("audio starved or backlog bypassed pacing")
				}
				// Continue publishing at the original rate while the pacer catches up.
				// This also exercises ticket-ring turnover beyond its retained capacity.
				continued := int(5 * time.Second / interval)
				for sequence := int(expectedWrites); sequence < int(expectedWrites)+continued; sequence++ {
					time.Sleep(interval)
					reorder.push(packet(uint16(sequence)), "video/VP8", time.Now(), emit)
				}
				expectedWrites += int32(continued)
				time.Sleep(2 * time.Second)
				synctest.Wait()
				got := p.diagnosticSnapshot(time.Now())
				if writes.Load() != expectedWrites || checks.Load() != expectedWrites || badOrder.Load() || got.QueuedPackets != 0 || got.DroppedQueueFull != 0 || got.DroppedExpired != 0 || got.DroppedScope != 0 {
					t.Fatalf("repair lost at egress: writes=%d checks=%d order=%v diagnostics=%+v", writes.Load(), checks.Load(), badOrder.Load(), got)
				}
				// Queued work still crosses final authority if permission changes later.
				p.SetTargetBitrate(0)
				denied := mediaTicket{delivery: MediaDelivery{Slot: SlotScreen}, guard: func(MediaDelivery, func() error) error { return nil }}
				prepared, ok := stream.prepare(packet(uint16(expectedWrites)), denied)
				if !ok {
					t.Fatal("denied fixture admission failed")
				}
				if _, err := p.Write(&prepared.Header, prepared.Payload, nil); err != nil {
					t.Fatal(err)
				}
				p.SetTargetBitrate(bitrate)
				time.Sleep(10 * time.Millisecond)
				synctest.Wait()
				if writes.Load() != expectedWrites || p.diagnosticSnapshot(time.Now()).DroppedScope != 1 {
					t.Fatal("final permission check bypassed")
				}
			})
		})
	}
}

func TestMediaPacerVideoStorageIsLazyAndAudioLifetimeUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		p.SetTargetBitrate(0)
		p.AddStream(1, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil }))
		p.mu.Lock()
		p.streams[1].audio = true
		p.mu.Unlock()
		if _, err := p.Write(&rtp.Header{SSRC: 1}, []byte{1}, nil); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		if p.queue != nil || time.Until(p.audioQueue[p.audioHead].expires) != 100*time.Millisecond {
			t.Error("voice allocated video storage or changed its deadline")
		}
		p.mu.Unlock()
		p.AddStream(2, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil }))
		if _, err := p.Write(&rtp.Header{SSRC: 2}, []byte{1}, nil); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		if len(p.queue) != mediaPacerPackets || time.Until(p.queue[p.head].expires) != mediaPacerLifetime {
			t.Error("video storage or deadline mismatch")
		}
		p.mu.Unlock()
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if p.queue != nil {
			t.Fatal("closed pacer retained video storage")
		}
	})
}

func TestMediaEgressVideoTicketWindowPreservesAudioExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := &mediaEgressRegistry{}
		stream := &mediaEgressStream{active: true, registry: registry}
		defer stream.stop()
		source := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1}, Payload: []byte{1}}
		video, _ := stream.prepare(source, mediaTicket{delivery: MediaDelivery{Slot: SlotScreen}})
		audio, _ := stream.prepare(source, mediaTicket{})
		// The whole pacer window plus repair headroom must not evict the first ticket.
		for range mediaPacerPackets - 1 {
			stream.prepare(source, mediaTicket{delivery: MediaDelivery{Slot: SlotScreen}})
		}
		if len(stream.tickets) != mediaPacerPackets+1 {
			t.Fatal("unexpected ticket retention")
		}
		writer := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil })
		time.Sleep(mediaTicketLifetime + time.Millisecond)
		if _, reason, _ := stream.writeResult(&audio.Header, audio.Payload, nil, writer); reason != mediaDropExpiredTicket {
			t.Fatalf("audio expiry changed: %v", reason)
		}
		if n, _, err := stream.writeResult(&video.Header, video.Payload, nil, writer); n != 1 || err != nil {
			t.Fatal("video ticket expired or evicted before paced delivery")
		}
		time.Sleep(mediaVideoTicketLifetime - mediaTicketLifetime)
		if _, reason, _ := stream.writeResult(&video.Header, video.Payload, nil, writer); reason != mediaDropExpiredTicket {
			t.Fatalf("video ticket did not expire: %v", reason)
		}
	})
}
