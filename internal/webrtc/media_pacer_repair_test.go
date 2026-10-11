package webrtc

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func repairTestHeader() rtp.Header {
	return rtp.Header{Version: 2, SSRC: 2, SequenceNumber: 900, Timestamp: 9000, CSRC: []uint32{0, 1}}
}

func TestQueuedRepairIdentityAndUnbind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		sink := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			t.Error("zero-budget video emitted")
			return 0, nil
		})
		p.AddStream(1, sink)
		p.addRTX(1, 2)
		p.AddStream(3, sink)
		p.addRTX(3, 4)
		write := func(h rtp.Header, osn byte) { _, _ = p.Write(&h, []byte{0, osn, 1}, nil) }
		h := repairTestHeader()
		for range 40 {
			write(h, 100)
		}
		// Primary packets are untouched even with matching RTP/ticket fields.
		primary := h.Clone()
		primary.SSRC = 1
		write(primary, 100)
		write(primary, 100)
		low, high, timestamp, other := h.Clone(), h.Clone(), h.Clone(), h.Clone()
		low.CSRC[1]++
		high.CSRC[0]++
		timestamp.Timestamp++
		other.SSRC = 4
		write(low, 100)
		write(high, 100)
		write(timestamp, 100)
		write(h, 101)
		write(other, 100)
		if got := p.diagnosticSnapshot(time.Now()); got.QueuedPackets != 8 || got.CoalescedRepairs != 39 || got.DroppedQueueFull != 0 {
			t.Fatalf("distinct authorization/stream/RTP identities were coalesced: %+v", got)
		}
		p.removeStream(1, 2)
		p.mu.Lock()
		if p.count != 1 || len(p.queuedRepairs) != 1 {
			t.Errorf("unbind retained repairs or removed another stream: queue=%d keys=%d", p.count, len(p.queuedRepairs))
		}
		p.mu.Unlock()
		// Reusing the SSRC with a new binding cannot match the retired stream.
		p.AddStream(1, sink)
		p.addRTX(1, 2)
		write(h, 100)
		if got := p.diagnosticSnapshot(time.Now()); got.QueuedPackets != 2 || got.CoalescedRepairs != 39 {
			t.Fatalf("new binding repair suppressed: %+v", got)
		}
		_ = p.Close()
		if len(p.queuedRepairs) != 0 {
			t.Fatal("closed pacer retained repair identities")
		}
	})
}

func TestQueuedRepairRequiresExactInternalTicketAndRTX(t *testing.T) {
	for _, scenario := range []string{"primary", "audio", "short payload", "missing ticket", "zero ticket", "extra CSRC"} {
		t.Run(scenario, func(t *testing.T) {
			h := repairTestHeader()
			stream := &pacedStream{rtxSSRC: 2}
			payload := []byte{0, 100}
			switch scenario {
			case "primary":
				h.SSRC = 1
			case "audio":
				stream.audio = true
			case "short payload":
				payload = payload[:1]
			case "missing ticket":
				h.CSRC = nil
			case "zero ticket":
				h.CSRC = []uint32{0, 0}
			case "extra CSRC":
				h.CSRC = append(h.CSRC, 1)
			}
			if key := queuedRepairKey(stream, &h, payload); key.stream != nil {
				t.Fatalf("ineligible repair received coalescing key: %+v", key)
			}
		})
	}
}

func TestQueuedRepairKeySeparatesRTXAndBinding(t *testing.T) {
	h := repairTestHeader()
	stream := &pacedStream{rtxSSRC: 2}
	original := queuedRepairKey(stream, &h, []byte{0, 100})
	stream.rtxSSRC, h.SSRC = 3, 3
	if other := queuedRepairKey(stream, &h, []byte{0, 100}); other == original {
		t.Fatal("different RTX SSRC matched old repair")
	}
	h.SSRC = 2
	if other := queuedRepairKey(&pacedStream{rtxSSRC: 2}, &h, []byte{0, 100}); other == original {
		t.Fatal("different sender binding matched old repair")
	}
}

func TestQueuedRepairRetiredBindingCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		sink := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			t.Error("zero-budget video emitted")
			return 0, nil
		})
		h := repairTestHeader()
		for range 2 {
			p.AddStream(1, sink)
			p.addRTX(1, 2)
			_, _ = p.Write(&h, []byte{0, 100}, nil)
		}
		time.Sleep(10 * time.Millisecond)
		if got := p.diagnosticSnapshot(time.Now()); got.QueuedPackets != 1 || got.DroppedRetired != 1 || got.CoalescedRepairs != 0 || len(p.queuedRepairs) != 1 {
			t.Fatalf("retired binding left repair key or suppressed replacement: %+v", got)
		}
	})
}

func TestQueuedRepairFullQueueExpiryAndRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		var written atomic.Int32
		p.AddStream(1, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			written.Add(1)
			return h.MarshalSize() + len(payload), nil
		}))
		p.addRTX(1, 2)
		h := repairTestHeader()
		write := func() { _, _ = p.Write(&h, []byte{0, 100, 1}, nil) }
		write()
		for range mediaPacerPackets - 1 {
			_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
		}
		for range 100 {
			write()
		}
		_, _ = p.Write(&h, []byte{0, 101, 1}, nil)
		before := p.diagnosticSnapshot(time.Now())
		if before.QueuedPackets != mediaPacerPackets || before.CoalescedRepairs != 100 || before.DroppedQueueFull != 1 {
			t.Fatalf("duplicates consumed full-queue budget: %+v", before)
		}
		time.Sleep(mediaPacerLifetime + 10*time.Millisecond)
		if got := p.diagnosticSnapshot(time.Now()); got.QueuedPackets != 0 || got.DroppedExpired != mediaPacerPackets || len(p.queuedRepairs) != 0 {
			t.Fatalf("expiry left repair state or changed lifetime bound: %+v", got)
		}
		p.SetTargetBitrate(1_000_000)
		write()
		time.Sleep(10 * time.Millisecond)
		write()
		time.Sleep(10 * time.Millisecond)
		if got := written.Load(); got != 2 || len(p.queuedRepairs) != 0 {
			t.Fatalf("later NACK could not retry dequeued/expired repair: writes=%d", got)
		}
		if before.CoalescedRepairs != 100 || before.QueuedPackets != mediaPacerPackets {
			t.Fatal("diagnostic snapshot retained mutable pacer state")
		}
	})
}

func TestNACKBurstCoalescesQueuedRTXWithoutSuppressingLaterRepair(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		var repairs atomic.Int32
		p.AddStream(1, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			if h.SSRC == 2 {
				repairs.Add(1)
			}
			return h.MarshalSize() + len(payload), nil
		}))
		p.addRTX(1, 2)
		factory, err := nack.NewResponderInterceptor()
		if err != nil {
			t.Fatal(err)
		}
		responder, err := factory.NewInterceptor("queued-repair")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = responder.Close() }()
		writer := responder.BindLocalStream(&interceptor.StreamInfo{SSRC: 1, SSRCRetransmission: 2, PayloadType: 96,
			PayloadTypeRetransmission: 97, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}, p)
		h := repairTestHeader()
		h.SSRC, h.SequenceNumber, h.PayloadType = 1, 100, 96
		if _, err := writer.Write(&h, []byte{1, 2, 3}, nil); err != nil {
			t.Fatal(err)
		}
		feedback, err := (&rtcp.TransportLayerNack{SenderSSRC: 99, MediaSSRC: 1, Nacks: []rtcp.NackPair{{PacketID: 100}}}).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		reader := responder.BindRTCPReader(interceptor.RTCPReaderFunc(func(buffer []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			return copy(buffer, feedback), attrs, nil
		}))
		request := func() {
			t.Helper()
			if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
				t.Fatal(err)
			}
		}
		for range 32 {
			request()
		}
		synctest.Wait()
		if got := p.diagnosticSnapshot(time.Now()); got.QueuedPackets != 2 || got.CoalescedRepairs != 31 {
			t.Fatalf("real NACK responder flooded queued RTX: %+v", got)
		}
		p.SetTargetBitrate(1_000_000)
		time.Sleep(10 * time.Millisecond)
		request()
		synctest.Wait()
		time.Sleep(10 * time.Millisecond)
		if got := repairs.Load(); got != 2 {
			t.Fatalf("expected one queued repair and one later retry, got %d", got)
		}
	})
}
