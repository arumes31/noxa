package webrtc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// Without a fresh publisher mapping an egress stream still reports, on the
// server clock like pion's default; the publisher mapping that arrives later
// takes over in a new, held-off generation.
func TestEgressTimingFallsBackToServerClock(t *testing.T) {
	now := time.Now()
	r, delivery := timingRouter(t, now) // no publisher SR yet
	s := &mediaEgressStream{active: true}
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 4800}, 10, timingTicket(r, delivery, 1, 4800), now)
	at := now.Add(100 * time.Millisecond)
	report, generation, ok := s.senderReport(48000, at)
	if !ok || generation != 1 {
		t.Fatalf("no server-clock SR without a publisher mapping: ok=%v gen=%d", ok, generation)
	}
	if report.SSRC != 99 || report.RTPTime != 4800+4800 || report.NTPTime != serverNTP(at) || report.PacketCount != 1 {
		t.Fatalf("server-clock SR %+v", report)
	}
	if !s.timing.commitReport(report, generation, at) {
		t.Fatal("server-clock SR not committed")
	}

	owner := &timingOwner{}
	r.sourceTiming.bind("pub", 1, owner)
	r.sourceTiming.observe("pub", owner, &rtcp.SenderReport{SSRC: 1, NTPTime: uint64(0xee000000) << 32, RTPTime: 4800}, 48000, at)
	switchAt := at.Add(time.Second)
	if _, _, ok := s.senderReport(48000, switchAt); ok {
		t.Fatal("clock-domain switch reported without a holdoff")
	}
	after := switchAt.Add(timingGenerationHoldoff)
	report, generation, ok = s.senderReport(48000, after)
	if !ok || generation != 2 {
		t.Fatalf("publisher mapping did not take over in a new generation: ok=%v gen=%d", ok, generation)
	}
	if want := ntpAdd(uint64(0xee000000)<<32, after.Sub(at)); report.NTPTime != want {
		t.Fatalf("publisher-clock SR ntp=%x want %x", report.NTPTime, want)
	}
	if !s.timing.commitReport(report, generation, after) {
		t.Fatal("publisher-clock SR not committed")
	}

	// The publisher stops reporting: back to the server clock, again as a
	// new generation.
	stale := after.Add(sourceTimingMaxAge + time.Second)
	s.timing.observeSent(&rtp.Header{SSRC: 99, Timestamp: 9600}, 10, timingTicket(r, delivery, 1, 9600), stale)
	if _, _, ok := s.senderReport(48000, stale); ok {
		t.Fatal("switch back reported without a holdoff")
	}
	if report, generation, ok = s.senderReport(48000, stale.Add(timingGenerationHoldoff)); !ok || generation != 3 || report.NTPTime != serverNTP(stale.Add(timingGenerationHoldoff)) {
		t.Fatalf("stale mapping did not fall back to the server clock: ok=%v gen=%d", ok, generation)
	}
}

type capturedRTCP struct {
	mu      sync.Mutex
	reports []*rtcp.SenderReport
}

func (c *capturedRTCP) Write(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, packet := range packets {
		if sr, ok := packet.(*rtcp.SenderReport); ok {
			c.reports = append(c.reports, sr)
		}
	}
	return 0, nil
}

// An outbound stream the egress registry does not manage gets pion's default
// server-clock SRs instead of none.
func TestUnmanagedOutputGetsServerClockReports(t *testing.T) {
	now := time.Now()
	clock := now
	factory := sourceTimedReportFactory{registry: &mediaEgressRegistry{streams: map[string]*mediaEgressStream{}}, interval: time.Hour, now: func() time.Time { return clock }}
	i, err := factory.NewInterceptor("")
	if err != nil {
		t.Fatal(err)
	}
	reporter := i.(*sourceTimedReporter)
	defer func() { _ = reporter.Close() }()
	writer := reporter.BindLocalStream(&interceptor.StreamInfo{ID: "tap", SSRC: 5, ClockRate: 48000},
		interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 0, nil }))
	if _, err := writer.Write(&rtp.Header{SSRC: 5, Timestamp: 960}, make([]byte, 20), nil); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(20 * time.Millisecond)
	out := &capturedRTCP{}
	reporter.writeReports(out)
	if len(out.reports) != 1 {
		t.Fatalf("unmanaged output got %d SRs, want 1", len(out.reports))
	}
	if sr := out.reports[0]; sr.SSRC != 5 || sr.RTPTime != 960+960 || sr.NTPTime != serverNTP(clock) || sr.PacketCount != 1 || sr.OctetCount != 20 {
		t.Fatalf("server-clock SR %+v", sr)
	}
}
