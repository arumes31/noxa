package webrtc

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestMediaPacerExpiredRepairDoesNotSpendPrimaryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, stream, original, writes := terminalDiagnosticFixture(t, mediaTicket{})
		p.bitrate = 8000
		p.stop, p.done = make(chan struct{}), make(chan struct{})
		go p.run()
		defer func() { _ = p.Close() }()
		// A cached original's authorization expires before its RTX is admitted.
		// Neither admission nor final authorization is bypassed by the fixture.
		time.Sleep(mediaTicketLifetime + time.Millisecond)
		repair := original
		repair.SSRC = 22
		repair.Payload = make([]byte, 1188)
		repair.Payload[1] = byte(original.SequenceNumber)
		fresh, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 11, SequenceNumber: 8}, Payload: make([]byte, 1188)}, mediaTicket{})
		if !ok {
			t.Fatal("fresh primary admission failed")
		}
		for _, packet := range []rtp.Packet{repair, fresh} {
			if _, err := p.Write(&packet.Header, packet.Payload, nil); err != nil {
				t.Fatal(err)
			}
		}
		// The next 5 ms tick must reject the repair and still send the primary.
		// At 8 kbps, a fictitious 1200-byte debit takes longer to repay than the
		// next pacing tick, so the primary would incorrectly remain queued.
		time.Sleep(4 * time.Millisecond)
		synctest.Wait()
		got := p.diagnosticSnapshot(time.Now())
		if writes.Load() != 1 || got.SentPackets != 1 || got.QueuedPackets != 0 || got.DroppedExpired != 0 ||
			got.DroppedScope != 1 || got.TerminalDrops.RTX.ExpiredTicket != 1 {
			t.Fatalf("expired repair spent primary pacing budget: writes=%d sent=%d queued=%d queueExpired=%d denied=%d repairExpired=%d", writes.Load(), got.SentPackets, got.QueuedPackets, got.DroppedExpired, got.DroppedScope, got.TerminalDrops.RTX.ExpiredTicket)
		}
	})
}

func TestMediaPacerChargesEveryAttemptIncludingErrors(t *testing.T) {
	for _, outcome := range []string{"success", "error", "zero-result"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newMediaPacer()
				defer func() { _ = p.Close() }()
				p.SetTargetBitrate(1000)
				writes := 0
				p.AddStream(11, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
					writes++
					switch outcome {
					case "error":
						return 0, errors.New("synthetic transport error")
					case "zero-result":
						return 0, nil
					default:
						return 1200, nil
					}
				}))
				for seq := uint16(1); seq <= 2; seq++ {
					if _, err := p.Write(&rtp.Header{Version: 2, SSRC: 11, SequenceNumber: seq}, make([]byte, 1188), nil); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(300 * time.Millisecond)
				synctest.Wait()
				if writes != 1 {
					t.Fatalf("attempt debt lost for %s: %d writes before repayment", outcome, writes)
				}
				// At 1kbps, one packet debt exceeds the whole bounded video lifetime.
				time.Sleep(mediaPacerLifetime - 300*time.Millisecond + 50*time.Millisecond)
				synctest.Wait()
				got := p.diagnosticSnapshot(time.Now())
				if writes != 1 || got.DroppedExpired != 1 || got.QueuedPackets != 0 {
					t.Fatalf("attempt must consume budget through original queue deadline: writes=%d diagnostics=%+v", writes, got)
				}
				wantFailures := uint64(0)
				if outcome == "error" {
					wantFailures = 1
				}
				if got.FinalWriteFailures != wantFailures || got.DroppedScope != 0 {
					t.Fatalf("attempt error classification changed: %+v", got)
				}
			})
		})
	}
}
