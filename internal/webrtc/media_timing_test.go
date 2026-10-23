package webrtc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestNTPArithmeticRoundTrips(t *testing.T) {
	base := uint64(0xee7d5acecf557fff)
	for _, d := range []time.Duration{0, time.Millisecond, -time.Millisecond, 1500 * time.Millisecond, -90 * time.Second, 37*time.Minute + 123*time.Microsecond} {
		got := ntpDuration(ntpAdd(base, d), base)
		if diff := got - d; diff > time.Microsecond || diff < -time.Microsecond {
			t.Fatalf("ntp round trip %v -> %v", d, got)
		}
	}
	m := sourceTimingMapping{ntp: base, rtp: 4294960000, clockRate: 90000}
	// Timestamp wrap-around keeps the line continuous.
	if got := m.timestampAt(m.captureTime(m.rtp + 9000)); got != m.rtp+9000 {
		t.Fatalf("mapping inverse across wrap: %d", got)
	}
	if d := ntpDuration(m.captureTime(m.rtp+90000), base); d != time.Second {
		t.Fatalf("one second of RTP maps to %v", d)
	}
}

// NTP additions wrap modulo 2^64, including the duration's minimum value.
func TestNTPAddMinimumDuration(t *testing.T) {
	const min = time.Duration(-1 << 63)
	const base = uint64(0xee7d5acecf557fff)
	// Split at a whole second to avoid introducing fractional rounding twice.
	want := ntpAdd(ntpAdd(base, min+time.Second), -time.Second)
	if got := ntpAdd(base, min); got != want {
		t.Fatalf("minimum duration: got %x, want %x", got, want)
	}
}

type timingOwner struct{ name string }

func TestSourceTimingOnlyCurrentOwnerDefinesMapping(t *testing.T) {
	var table sourceTimingTable
	old, replacement := &timingOwner{"old"}, &timingOwner{"new"}
	now := time.Now()
	table.bind("pub", 7, old)
	if !table.observe("pub", old, &rtcp.SenderReport{SSRC: 7, NTPTime: 1 << 40, RTPTime: 100}, 90000, now) {
		t.Fatal("bound owner rejected")
	}
	// The same SSRC is taken over by a replacement track before the old
	// reader delivers a late report.
	table.bind("pub", 7, replacement)
	if table.observe("pub", old, &rtcp.SenderReport{SSRC: 7, NTPTime: 2 << 40, RTPTime: 200}, 90000, now) {
		t.Fatal("late SR of a replaced track defined the successor's mapping")
	}
	if _, ok := table.mapping("pub", 7, now); ok {
		t.Fatal("replacement inherited the previous track's mapping")
	}
	table.unbind("pub", 7, old) // stale unbind must not remove the replacement
	if !table.observe("pub", replacement, &rtcp.SenderReport{SSRC: 7, NTPTime: 3 << 40, RTPTime: 300}, 90000, now) {
		t.Fatal("replacement owner rejected")
	}
	// Reordered reports never move the mapping backwards.
	if table.observe("pub", replacement, &rtcp.SenderReport{SSRC: 7, NTPTime: 2 << 40, RTPTime: 250}, 90000, now) {
		t.Fatal("older SR replaced a newer mapping")
	}
	// A publisher clock step back (RTP still advancing) must not freeze the
	// mapping until it ages out.
	if !table.observe("pub", replacement, &rtcp.SenderReport{SSRC: 7, NTPTime: 1 << 40, RTPTime: 400}, 90000, now) {
		t.Fatal("publisher clock step froze the mapping")
	}
	if m, ok := table.mapping("pub", 7, now.Add(sourceTimingMaxAge+time.Second)); ok {
		t.Fatalf("stale mapping used: %+v", m)
	}
	table.forgetPublisher("pub")
	if _, ok := table.mapping("pub", 7, now); ok {
		t.Fatal("publisher detach kept its mappings")
	}
}

func TestSourceSwitchPreservesOutboundCaptureMapping(t *testing.T) {
	var table sourceTimingTable
	now := time.Now()
	a, b := &timingOwner{"a"}, &timingOwner{"b"}
	table.bind("pub", 1, a)
	table.bind("pub", 2, b)
	// Both sources were captured on one publisher clock with unrelated RTP bases.
	ntp0 := uint64(0xee000000) << 32
	table.observe("pub", a, &rtcp.SenderReport{SSRC: 1, NTPTime: ntp0, RTPTime: 1000}, 90000, now)
	table.observe("pub", b, &rtcp.SenderReport{SSRC: 2, NTPTime: ntp0, RTPTime: 777777}, 90000, now)
	const oldOffset = 5000
	// A frame of source b captured 2 s after the SR instant.
	newTS := uint32(777777 + 2*90000)
	offset, ok := table.preservingOffset("pub", 1, oldOffset, 2, newTS, now)
	if !ok {
		t.Fatal("known mappings did not produce a preserving offset")
	}
	if want := uint32(1000 + 2*90000 + oldOffset); newTS+offset != want {
		t.Fatalf("outbound timestamp %d, want %d (same capture instant on the old line)", newTS+offset, want)
	}
	if !table.outputMappingsEqual("pub", 1, oldOffset, 2, offset, now) {
		t.Fatal("preserved offset changed the outbound mapping")
	}
	// Same SSRC (re-watch) keeps its offset without any SR.
	if offset, ok := table.preservingOffset("pub", 3, 42, 3, 9, now); !ok || offset != 42 {
		t.Fatalf("re-watch offset %d, %v", offset, ok)
	}
	// Unknown new source: no preserving offset (the caller falls back).
	if _, ok := table.preservingOffset("pub", 1, oldOffset, 4, 1, now); ok {
		t.Fatal("offset invented for a source without SR")
	}
}

func TestContinuitySwitchUsesPreservingOffsetOnlyForward(t *testing.T) {
	now := time.Now()
	v := videoContinuity{seen: true, ssrc: 1, timestamp: 100000, timestampOffset: 10, last: now.Add(-time.Second)}
	pkt := &rtp.Packet{Header: rtp.Header{SSRC: 2, Timestamp: 50000}}
	v.preferredOffset, v.preferredOffsetOK = 60000, true // forward step 10000
	if got := v.switchTimestampOffset(pkt, now); got != 60000 {
		t.Fatalf("forward preserving offset ignored: %d", got)
	}
	v.preferredOffset = 40000 // would step backwards to 90000
	elapsed := v.switchTimestampOffset(pkt, now)
	if elapsed == 40000 || 50000+elapsed-100000 < 89000 || 50000+elapsed-100000 > 91000 {
		t.Fatalf("backwards preserving offset not replaced by elapsed offset: %d", elapsed)
	}
	v.preferredOffsetOK = false
	if got := v.switchTimestampOffset(pkt, now); got != elapsed {
		t.Fatalf("fallback offset %d != %d", got, elapsed)
	}
}

// timingRouter returns a router with an authorized mic delivery from pub to
// viewer and a publisher SR mapping for the given source SSRCs.
func timingRouter(t *testing.T, now time.Time, ssrcs ...uint32) (*Router, MediaDelivery) {
	t.Helper()
	r := NewRouter(nil)
	r.JoinChannel(1, "pub")
	r.JoinChannel(1, "viewer")
	token, ok := r.claimSlot("pub", SlotMic)
	if !ok {
		t.Fatal("claim")
	}
	r.mu.RLock()
	delivery := r.mediaOutputLocked("pub", "viewer", SlotMic, false, nil).delivery
	r.mu.RUnlock()
	if delivery.SourceEpoch != token {
		t.Fatal("delivery source epoch")
	}
	for i, ssrc := range ssrcs {
		owner := &timingOwner{}
		r.sourceTiming.bind("pub", ssrc, owner)
		r.sourceTiming.observe("pub", owner, &rtcp.SenderReport{SSRC: ssrc, NTPTime: uint64(0xee000000)<<32 | 0x12345678, RTPTime: uint32(i) * 1_000_000}, 48000, now)
	}
	return r, delivery
}

func timingTicket(r *Router, delivery MediaDelivery, source, sourceTS uint32) *mediaTicket {
	return &mediaTicket{router: r, delivery: delivery, timingSSRC: source, timingTimestamp: sourceTS}
}

func TestEgressTimingGenerationsAndLateReports(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now, 1, 2)
	s := &mediaEgressStream{active: true}
	send := func(source, sourceTS, out uint32, at time.Time) {
		s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: out}, 10, timingTicket(r, delivery, source, sourceTS), at)
	}
	send(1, 480, 480, now)
	report, generation, ok := s.senderReport(48000, now)
	if !ok || generation != 1 || report.SSRC != 99 || report.RTPTime != 0 || report.NTPTime != uint64(0xee000000)<<32|0x12345678 {
		t.Fatalf("first generation SR: %+v gen=%d ok=%v", report, generation, ok)
	}
	// A report computed for generation 1 ...
	late, lateGeneration, _ := s.senderReport(48000, now.Add(10*time.Millisecond))
	// ... is overtaken by a discontinuous switch to source 2.
	send(2, 1_000_480, 960, now.Add(20*time.Millisecond))
	if s.timing.commitReport(late, lateGeneration, now.Add(30*time.Millisecond)) {
		t.Fatal("a late SR of the previous generation was committed after the switch")
	}
	// The new generation waits for its holdoff, then reports source 2's line.
	if _, _, ok := s.senderReport(48000, now.Add(30*time.Millisecond)); ok {
		t.Fatal("new generation reported during its holdoff")
	}
	at := now.Add(20*time.Millisecond + timingGenerationHoldoff)
	report, generation, ok = s.senderReport(48000, at)
	if !ok || generation != 2 {
		t.Fatalf("new generation never reported: gen=%d ok=%v", generation, ok)
	}
	// Outbound 960 is source-2 timestamp 1_000_480, i.e. 10 ms after its SR.
	elapsed := at.Sub(now)
	wantRTP := uint32(960-480) + uint32(rtpTicks(elapsed, 48000))
	if report.RTPTime != wantRTP {
		t.Fatalf("generation 2 SR rtp=%d want %d", report.RTPTime, wantRTP)
	}
	if !s.timing.commitReport(report, generation, at) {
		t.Fatal("current report rejected")
	}
	// Going back to source 1 after a partially failed switch is a fresh
	// generation, never the reused number 1, and it reports again.
	send(1, 960, 2000, at.Add(time.Millisecond))
	_, _, _ = s.senderReport(48000, at)
	s.timing.mu.Lock()
	third := s.timing.generation
	s.timing.mu.Unlock()
	if third != 3 {
		t.Fatalf("recovery generation %d, want fresh 3", third)
	}
	if _, g, ok := s.senderReport(48000, at.Add(time.Millisecond+timingGenerationHoldoff)); !ok || g != 3 {
		t.Fatal("recovered generation does not report: receiver would never get a mapping")
	}
}

func TestEgressTimingContinuousSwitchKeepsGeneration(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now, 1, 2)
	s := &mediaEgressStream{active: true}
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 5000}, 10, timingTicket(r, delivery, 1, 5000), now)
	// Source 2's line is source 1's shifted by 1_000_000; its offset preserves
	// the outbound mapping exactly, so no new generation (and no holdoff).
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 6000}, 10, timingTicket(r, delivery, 2, 1_006_000), now.Add(time.Millisecond))
	if _, generation, ok := s.senderReport(48000, now.Add(2*time.Millisecond)); !ok || generation != 1 {
		t.Fatalf("mapping-preserving switch started generation %d (ok=%v)", generation, ok)
	}
}

func TestEgressTimingStopsWhenRevokedOrIdle(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now, 1)
	s := &mediaEgressStream{active: true}
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 1}, 10, timingTicket(r, delivery, 1, 1), now)
	if _, _, ok := s.senderReport(48000, now); !ok {
		t.Fatal("authorized source not reported")
	}
	if _, _, ok := s.senderReport(48000, now.Add(senderReportIdle+time.Millisecond)); ok {
		t.Fatal("idle stream kept sending SRs")
	}
	r.SetHandlers(func(string) bool { return false }, nil) // talk power revoked
	if _, _, ok := s.senderReport(48000, now); ok {
		t.Fatal("SR sent after talk power was revoked")
	}
	r.SetHandlers(nil, nil)
	r.releaseSlot("pub", SlotMic, delivery.SourceEpoch) // track ended
	if _, _, ok := s.senderReport(48000, now); ok {
		t.Fatal("SR sent after the source track was released")
	}
	if !s.stop() {
		t.Fatal("stop")
	}
	if _, _, ok := s.senderReport(48000, now); ok {
		t.Fatal("SR sent on a stopped output")
	}
}

func TestEgressTimingRoundTripUsesServerSendTime(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now, 1)
	s := &mediaEgressStream{active: true}
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 1}, 10, timingTicket(r, delivery, 1, 1), now)
	report, generation, ok := s.senderReport(48000, now)
	if !ok || !s.timing.commitReport(report, generation, now) {
		t.Fatal("report")
	}
	// The receiver held the SR for 100 ms; the path took 40 ms round trip.
	s.timing.observeReception(rtcp.ReceptionReport{SSRC: 99, LastSenderReport: uint32(report.NTPTime >> 16), Delay: 65536 / 10}, now.Add(140*time.Millisecond))
	rtt, at := s.timing.roundTrip()
	if at.IsZero() || rtt < 39*time.Millisecond || rtt > 41*time.Millisecond {
		t.Fatalf("rtt %v (the SR carried a publisher clock far from the server's)", rtt)
	}
}

// SR generation, admission and the fanout race on shared timing state.
func TestEgressTimingConcurrentUse(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now, 1, 2)
	s := &mediaEgressStream{active: true}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				source := uint32(1 + (i/100+w)%2)
				s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: uint32(i)}, 10, timingTicket(r, delivery, source, uint32(i)), time.Now())
				if report, generation, ok := s.senderReport(48000, time.Now()); ok {
					s.timing.commitReport(report, generation, time.Now())
				}
				s.timing.observeReception(rtcp.ReceptionReport{SSRC: 99, LastSenderReport: uint32(i)}, time.Now())
				r.sourceTiming.preservingOffset("pub", 1, 0, 2, uint32(i), time.Now())
			}
		}(w)
	}
	wg.Wait()
}

// waitAsyncKeyframeRequests waits for media-path PLI writes, which run on
// their own goroutines so a slow publisher transport cannot stall fanout.
// Observing the drained set under its lock orders those writes before the
// caller's assertions.
func waitAsyncKeyframeRequests(t *testing.T, r *Router) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.keyframeWriteMu.Lock()
		pending := len(r.keyframeWriting)
		r.keyframeWriteMu.Unlock()
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("asynchronous keyframe request did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
