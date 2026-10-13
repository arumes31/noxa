package webrtc

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"noxa/internal/netproto"
)

func TestVideoRepairDiagnosticGapBuckets(t *testing.T) {
	d := &videoForwardDiagnostic{repairEpoch: 3}
	d.mu.Lock()
	defer d.mu.Unlock()
	ticket := &videoRepairTicketDiagnostic{}
	now := time.Unix(1, 0)
	d.observeRepair(ticket, now)
	for _, gap := range []time.Duration{5 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, time.Second, time.Second + time.Microsecond} {
		now = now.Add(gap)
		d.observeRepair(ticket, now)
	}
	if d.repairs.FirstRepairs != 1 || d.repairs.RepeatRepairs != 7 || d.repairs.MaxRepeatGapUS != 1000001 || d.repairs.RepeatGapHistogram != [7]uint64{1, 1, 1, 1, 1, 1, 1} {
		t.Fatalf("incorrect successful-repair histogram: %+v", d.repairs)
	}
	// A ticket retained across a diagnostic epoch cannot become a repeat in
	// the new epoch just because its authorization lifetime has not expired.
	d.repairEpoch++
	d.repairs = netproto.VideoRTXRepairDiagnostics{}
	d.observeRepair(ticket, now)
	if d.repairs.FirstRepairs != 1 || d.repairs.RepeatRepairs != 0 {
		t.Fatal("old diagnostic receipt contaminated new epoch")
	}
}

func TestVideoRepairDiagnosticsActualPacerClassificationAndSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := &mediaEgressRegistry{}
		stream := &mediaEgressStream{active: true, registry: registry}
		stream.outputSSRC.Store(42)
		registry.streams = map[string]*mediaEgressStream{"test": stream}
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.egress = registry
		allowed, zero, fail := true, false, false
		p.AddStream(42, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			if fail {
				return 1, errors.New("synthetic write failure")
			}
			if zero {
				return 0, nil
			}
			return h.MarshalSize() + len(payload), nil
		}))
		p.mu.Lock()
		p.streams[42].bindingID = "test"
		p.mu.Unlock()
		p.addRTX(42, 43)
		source := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: 100, Timestamp: 9000}, Payload: []byte{1, 2}}
		ticket := mediaTicket{videoSource: 99, delivery: MediaDelivery{Slot: SlotScreen, Publication: 5, WatchEpoch: 6}, guard: func(_ MediaDelivery, write func() error) error {
			if allowed {
				return write()
			}
			return nil
		}}
		packet, ok := stream.prepare(source, ticket)
		if !ok {
			t.Fatal("ticket admission failed")
		}
		write := func(h rtp.Header, payload []byte) {
			t.Helper()
			if _, err := p.Write(&h, payload, nil); err != nil {
				t.Fatal(err)
			}
		}
		drain := func() { time.Sleep(5 * time.Millisecond); synctest.Wait() }
		counts := func() netproto.VideoRTXRepairDiagnostics {
			stream.videoDiagnostic.mu.Lock()
			defer stream.videoDiagnostic.mu.Unlock()
			return stream.videoDiagnostic.repairs
		}
		assert := func(first, repeat uint64) {
			t.Helper()
			got := counts()
			if got.FirstRepairs != first || got.RepeatRepairs != repeat {
				t.Fatalf("repairs=%+v want first=%d repeat=%d", got, first, repeat)
			}
		}
		write(packet.Header, packet.Payload)
		drain()
		rtx := packet.Header.Clone()
		rtx.SSRC = 43
		payload := []byte{0, 100, 1, 2}
		write(rtx, payload)
		write(rtx, payload)
		drain()
		assert(1, 0)
		if p.diagnosticSnapshot(time.Now()).CoalescedRepairs != 1 {
			t.Fatal("queued duplicate was not coalesced")
		}
		write(rtx, payload)
		drain()
		assert(1, 1)
		write(packet.Header, packet.Payload) // same-SSRC retransmission stays primary
		padding := rtx.Clone()
		padding.Padding = true
		padding.PaddingSize = 8
		write(padding, nil)
		drain()
		assert(1, 1)
		// RTX wrapping source padding has an OSN payload, hence pacer kindRTX,
		// but its authorization ticket still identifies it as nonmedia.
		sourcePadding := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, Padding: true, PaddingSize: 8}}
		paddingPacket, paddingOK := stream.prepare(sourcePadding, ticket)
		if !paddingOK {
			t.Fatal("padding ticket admission failed")
		}
		wrappedPadding := paddingPacket.Header.Clone()
		wrappedPadding.SSRC = 43
		wrappedPadding.Padding = false
		wrappedPadding.PaddingSize = 0
		write(wrappedPadding, []byte{0, 101})
		drain()
		assert(1, 1)
		allowed = false
		write(rtx, payload)
		drain()
		assert(1, 1)
		allowed = true
		zero = true
		write(rtx, payload)
		drain()
		assert(1, 1)
		zero = false
		// Ticket expiry, unlike a pacer queue timeout, is checked at final write.
		time.Sleep(mediaVideoTicketLifetime)
		write(rtx, payload)
		drain()
		assert(1, 1)
		packet, ok = stream.prepare(source, ticket)
		if !ok {
			t.Fatal("fresh admission failed")
		}
		rtx = packet.Header.Clone()
		rtx.SSRC = 43
		write(rtx, payload)
		drain()
		assert(2, 1) // same OSN, new authorization ticket
		fail = true
		write(rtx, payload)
		drain()
		assert(2, 1)
		write(rtx, payload)
		drain()
		assert(2, 1) // failed writer retired the stream
	})
}

func TestVideoRepairDiagnosticsResetSourcePublicationAndWatch(t *testing.T) {
	for _, change := range []string{"source", "publication", "watch"} {
		t.Run(change, func(t *testing.T) {
			s := &mediaEgressStream{}
			s.outputSSRC.Store(42)
			old := mediaTicket{id: 1, videoSource: 99, videoMedia: true, delivery: MediaDelivery{Slot: SlotScreen, Publication: 5, WatchEpoch: 6}}
			primary := &rtp.Header{SSRC: 42, Timestamp: 9000}
			repair := &rtp.Header{SSRC: 43, Timestamp: 9000}
			s.observeVideo(primary, 100, &old, time.Now())
			s.observeVideoKind(repair, 100, &old, time.Now(), mediaPacketRTX)
			s.observeVideoKind(repair, 100, &old, time.Now(), mediaPacketRTX)
			current := mediaTicket{id: 2, videoSource: old.videoSource, videoMedia: true, delivery: old.delivery}
			switch change {
			case "source":
				current.videoSource++
			case "publication":
				current.delivery.Publication++
			case "watch":
				current.delivery.WatchEpoch++
			}
			s.observeVideo(primary, 100, &current, time.Now())
			s.observeVideoKind(repair, 100, &old, time.Now(), mediaPacketRTX)
			if s.videoDiagnostic.repairs != (netproto.VideoRTXRepairDiagnostics{}) {
				t.Fatal("retired repair changed fresh epoch counters")
			}
			s.observeVideoKind(repair, 100, &current, time.Now(), mediaPacketRTX)
			if s.videoDiagnostic.repairs.FirstRepairs != 1 || s.videoDiagnostic.repairs.RepeatRepairs != 0 {
				t.Fatal("fresh epoch inherited repair receipt")
			}
		})
	}
}

func TestVideoOperatorRepairSnapshotIsScopedAndIndependent(t *testing.T) {
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	generation, err := r.PublishVideo("pub", SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("viewer")
	if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 1, session, true); err != nil {
		t.Fatal(err)
	}
	watch := r.watches[watchKey{"viewer", "pub", SlotScreen}]
	s := &mediaEgressStream{active: true}
	s.outputSSRC.Store(42)
	r.pubTracks["viewer"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotScreen: {egress: s}}}}
	ticket := &mediaTicket{id: 1, videoSource: 99, videoMedia: true, delivery: MediaDelivery{Slot: SlotScreen, Publication: generation, WatchEpoch: watch.epoch}}
	// A successful RTX observation cannot fabricate a forwarding epoch or
	// present unsupported zero counters before the first primary write.
	s.observeVideoKind(&rtp.Header{SSRC: 43}, 100, ticket, time.Now(), mediaPacketRTX)
	if r.VideoOperatorDiagnostics().Publications[0].Viewers[0].RTXRepairs != nil || !s.videoDiagnostic.stage.last.IsZero() {
		t.Fatal("RTX fabricated initial forwarding diagnostics")
	}
	s.observeVideo(&rtp.Header{SSRC: 42}, 100, ticket, time.Now())
	s.observeVideoKind(&rtp.Header{SSRC: 43}, 100, ticket, time.Now(), mediaPacketRTX)
	snapshot := r.VideoOperatorDiagnostics().Publications[0].Viewers[0].RTXRepairs
	if snapshot == nil || snapshot.FirstRepairs != 1 {
		t.Fatal("current operator repair snapshot missing")
	}
	snapshot.FirstRepairs = 900
	snapshot.RepeatGapHistogram[0] = 900
	next := r.VideoOperatorDiagnostics().Publications[0].Viewers[0].RTXRepairs
	if next.FirstRepairs != 1 || next.RepeatGapHistogram[0] != 0 {
		t.Fatal("operator snapshot aliases mutable receipt state")
	}
	if _, err = r.WatchVideo("viewer", "pub", SlotScreen, generation, 2, session, false); err != nil {
		t.Fatal(err)
	}
	if r.VideoOperatorDiagnostics().Publications[0].Viewers[0].RTXRepairs != nil {
		t.Fatal("unwatch retained current repair diagnostic")
	}
}

func TestVideoRepairDiagnosticPrepareResetsCopiedReceipt(t *testing.T) {
	stream := &mediaEgressStream{active: true, registry: &mediaEgressRegistry{}}
	input := mediaTicket{repairDiagnostic: videoRepairTicketDiagnostic{epoch: 9, seen: true, last: time.Unix(1, 0)}}
	packet, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2}}, input)
	if !ok {
		t.Fatal("ticket admission failed")
	}
	id := uint64(packet.CSRC[0])<<32 | uint64(packet.CSRC[1])
	stream.ticketsMu.Lock()
	got := stream.tickets[id].repairDiagnostic
	stream.ticketsMu.Unlock()
	if got != (videoRepairTicketDiagnostic{}) || !input.repairDiagnostic.seen {
		t.Fatal("new ticket inherited receipt state or changed caller copy")
	}
}
