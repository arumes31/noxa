package webrtc

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
)

// A/V sync across GOP cache starts. A viewer started from the cache receives
// the cached frames under a timestamp offset chosen for it alone (a refill of
// a playing viewer shifts its timeline forward, a new continuity epoch). Its
// Sender Reports must carry the publisher's capture mapping shifted by exactly
// the offset applied to the packets it was sent; any other offset moves its
// video against the publisher's audio (lip sync).
func TestGOPCacheStartKeepsSenderReportCaptureMapping(t *testing.T) {
	verifyNoLeaks(t)
	// Periodic keyframes keep the cache servable (gopCacheMaxAge) across both
	// refills, which are spaced by gopRefillInterval.
	f, arrivals := startGOPFixture(t, 1200*time.Millisecond)
	r := f.router
	const source = uint32(4242) // startGOPFixture's publisher SSRC
	owner := &timingOwner{name: "cam"}
	r.sourceTiming.bind("pub", source, owner)
	if !r.sourceTiming.observe("pub", owner, &rtcp.SenderReport{SSRC: source, NTPTime: uint64(0xee112233)<<32 | 0x40000000, RTPTime: 777_000}, 90000, time.Now()) {
		t.Fatal("publisher SR not accepted")
	}
	publisher, _ := r.sourceTiming.mapping("pub", source, time.Now())

	time.Sleep(100 * time.Millisecond)
	f.watch(t)
	receiveDecodable(t, arrivals, 300*time.Millisecond)

	state := func() (offset uint32, generation uint64) {
		f.output.videoMu.Lock()
		offset = f.output.videoContinuity.timestampOffset
		f.output.videoMu.Unlock()
		f.output.egress.timing.mu.Lock()
		generation = f.output.egress.timing.generation
		f.output.egress.timing.mu.Unlock()
		return offset, generation
	}
	// checkReports verifies the SR the viewer is sent now against the offset
	// actually applied to the frames it received.
	checkReports := func(offset uint32, generation uint64) {
		t.Helper()
		at := time.Now().Add(timingGenerationHoldoff)
		report, reportGeneration, ok := f.output.egress.senderReport(90000, at)
		if !ok || reportGeneration != generation {
			t.Fatalf("no SR for generation %d (got %d, ok=%v)", generation, reportGeneration, ok)
		}
		// The viewer maps outbound RTP to capture time with this SR; it must
		// be the publisher's own mapping plus the viewer's offset.
		want := publisher.timestampAt(report.NTPTime) + offset
		if diff := int32(report.RTPTime - want); diff < -1 || diff > 1 {
			t.Fatalf("SR maps NTP %x to RTP %d, publisher capture mapping + offset %d gives %d", report.NTPTime, report.RTPTime, offset, want)
		}
	}
	offset, generation := state()
	if offset != 0 || generation != 1 {
		t.Fatalf("cache join: offset %d generation %d, want the source timeline (0, 1)", offset, generation)
	}
	checkReports(offset, generation)

	// Each refill restarts this viewer from the cache under a new offset.
	// Every packet sent for it, cached and live, carries that offset: exactly
	// one timing generation per refill.
	for refill := 1; refill <= 2; refill++ {
		before := f.output.gopRefilledAt.Load()
		deadline := time.Now().Add(3 * time.Second)
		for f.output.gopRefilledAt.Load() == before {
			if time.Now().After(deadline) {
				t.Fatalf("refill %d was not served from the cache", refill)
			}
			if err := f.receiver.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: f.output.egress.outputSSRC.Load()}}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond) // backfill and following live frames sent
		next, nextGeneration := state()
		if next == offset {
			t.Fatalf("refill %d kept offset %d: the cached keyframe would step timestamps backwards", refill, next)
		}
		if nextGeneration != generation+1 {
			t.Fatalf("refill %d: timing generation %d -> %d, want exactly one new generation (cached packets reported another offset)", refill, generation, nextGeneration)
		}
		offset, generation = next, nextGeneration
		checkReports(offset, generation)
		// Received frames: outbound timestamp minus the reported offset is a
		// publisher capture timestamp (the fixture stamps frame*1500).
		replayed := 0
	drain:
		for {
			select {
			case a := <-arrivals:
				if a.packet.Timestamp-offset == 0 || (a.packet.Timestamp-offset)%1500 != 0 {
					continue // sent before this refill under the previous offset
				}
				replayed++
			default:
				break drain
			}
		}
		if replayed == 0 {
			t.Fatalf("refill %d: no received frame maps to a publisher timestamp under offset %d", refill, offset)
		}
	}
	if plis, _ := f.upstream.counts(); plis != 0 {
		t.Fatalf("refills reached the publisher: %d requests", plis)
	}
}
