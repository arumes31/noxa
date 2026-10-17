package webrtc

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// A synchronous real pacer queue/terminal-write path, without timers: tests can
// expire/revoke a ticket between admission and final write deterministically.
func terminalDiagnosticFixture(t *testing.T, ticket mediaTicket) (*mediaPacer, *mediaEgressStream, rtp.Packet, *atomic.Int64) {
	t.Helper()
	registry := &mediaEgressRegistry{streams: make(map[string]*mediaEgressStream)}
	stream := &mediaEgressStream{active: true, registry: registry}
	registry.streams["test"] = stream
	p := &mediaPacer{streams: make(map[uint32]*pacedStream), egress: registry}
	writes := new(atomic.Int64)
	p.AddStream(11, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		writes.Add(1)
		return h.MarshalSize() + len(payload), nil
	}))
	p.addRTX(11, 22)
	p.streams[11].bindingID = "test"
	packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 11, SequenceNumber: 7}, Payload: []byte{1}}, ticket)
	if !ok {
		t.Fatal("ticket preparation failed")
	}
	return p, stream, packet, writes
}

func flushTerminalDiagnosticPacket(t *testing.T, p *mediaPacer, packet rtp.Packet) {
	t.Helper()
	if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	queued := p.popLocked()
	p.mu.Unlock()
	p.writePacket(queued)
}

func TestTerminalDropReasonsPreserveDeniedWrites(t *testing.T) {
	for _, name := range []string{"binding", "invalid", "missing", "expired", "inactive", "policy_busy", "policy_revision", "scope", "guard", "guard_error", "evicted_during_guard"} {
		t.Run(name, func(t *testing.T) {
			r := NewRouter(nil)
			ticket := mediaTicket{router: r, videoRevision: r.videoPolicySnapshot().revision, delivery: MediaDelivery{Slot: SlotCam, Tap: true}}
			p, stream, packet, writes := terminalDiagnosticFixture(t, ticket)
			expected := mediaDropUnclassified
			switch name {
			case "binding":
				delete(p.egress.streams, "test")
				expected = mediaDropMissingBinding
			case "invalid":
				packet.CSRC = nil
				expected = mediaDropInvalidTicket
			case "missing":
				clear(stream.tickets)
				expected = mediaDropMissingTicket
			case "expired":
				stream.tickets[1].expires = time.Now().Add(-time.Second)
				expected = mediaDropExpiredTicket
			case "inactive":
				stream.stop()
				expected = mediaDropInactiveStream
			case "policy_busy":
				r.videoPolicyMu.Lock()
				defer r.videoPolicyMu.Unlock()
				expected = mediaDropPolicyBusy
			case "policy_revision":
				if err := r.SetVideoLimits(123, VideoBounds{}); err != nil {
					t.Fatal(err)
				}
				expected = mediaDropPolicyRevision
			case "scope":
				stream.tickets[1].delivery.Tap = false // No active watch grants this packet.
				expected = mediaDropScopeDeclined
			case "guard", "guard_error":
				stream.tickets[1].guard = func(MediaDelivery, func() error) error {
					if name == "guard_error" {
						return errors.New("permission unavailable")
					}
					return nil
				}
				expected = mediaDropGuardDeclined
			case "evicted_during_guard":
				stream.tickets[1].guard = func(_ MediaDelivery, write func() error) error {
					stream.mu.Lock()
					clear(stream.tickets)
					stream.mu.Unlock()
					return write()
				}
				expected = mediaDropMissingTicket
			}
			flushTerminalDiagnosticPacket(t, p, packet)
			got := p.diagnosticSnapshot(time.Now())
			if writes.Load() != 0 || got.DroppedScope != 1 || got.FinalWriteFailures != 0 || p.diagnostics.terminalDrops[mediaPacketPrimary][expected] != 1 {
				t.Fatalf("denial changed behavior or was misclassified: writes=%d diagnostic=%+v reasons=%+v", writes.Load(), got, got.TerminalDrops)
			}
			var total uint64
			for _, reasons := range p.diagnostics.terminalDrops {
				for _, count := range reasons {
					total += count
				}
			}
			if total != got.DroppedScope {
				t.Fatal("one rejected packet was counted more than once")
			}
		})
	}
}

func TestTerminalDropsSeparateLateRTXFromOriginalAndPadding(t *testing.T) {
	p, stream, packet, writes := terminalDiagnosticFixture(t, mediaTicket{})
	flushTerminalDiagnosticPacket(t, p, packet)
	if writes.Load() != 1 || p.diagnosticSnapshot(time.Now()).DroppedScope != 0 {
		t.Fatal("fresh original was not sent")
	}
	stream.tickets[1].expires = time.Now().Add(-time.Second)
	// Pion RTX preserves the authorization ticket in CSRC, uses its negotiated
	// SSRC and prepends the original sequence number to its payload.
	repair := packet
	repair.SSRC, repair.Payload = 22, []byte{0, 7, 1}
	flushTerminalDiagnosticPacket(t, p, repair)
	flushTerminalDiagnosticPacket(t, p, packet)
	padding := packet
	padding.Padding, padding.PaddingSize, padding.Payload = true, 12, nil
	flushTerminalDiagnosticPacket(t, p, padding)
	got := p.diagnosticSnapshot(time.Now())
	if writes.Load() != 1 || got.SentPackets != 1 || got.DroppedScope != 3 || got.TerminalDrops.RTX.ExpiredTicket != 1 ||
		got.TerminalDrops.Primary.ExpiredTicket != 1 || got.TerminalDrops.Padding.ExpiredTicket != 1 {
		t.Fatalf("late repair was conflated with original loss: %+v", got.TerminalDrops)
	}
	// Replacement output gets fresh tickets but recipient totals retain history.
	replacement := &mediaEgressStream{active: true, registry: stream.registry}
	p.egress.streams["test"] = replacement
	flushTerminalDiagnosticPacket(t, p, repair)
	fresh, ok := replacement.prepare(&packet, mediaTicket{})
	if !ok {
		t.Fatal("replacement ticket refused")
	}
	flushTerminalDiagnosticPacket(t, p, fresh)
	got = p.diagnosticSnapshot(time.Now())
	if writes.Load() != 2 || got.DroppedScope != 4 || got.TerminalDrops.RTX.MissingTicket != 1 || got.TerminalDrops.RTX.ExpiredTicket != 1 {
		t.Fatal("replacement output reset history or admitted an old repair")
	}
	got.TerminalDrops.RTX.ExpiredTicket = 999
	if p.diagnosticSnapshot(time.Now()).TerminalDrops.RTX.ExpiredTicket != 1 || (&mediaPacer{}).diagnosticSnapshot(time.Now()).DroppedScope != 0 {
		t.Fatal("snapshot aliases counters or a new peer inherits history")
	}
}
