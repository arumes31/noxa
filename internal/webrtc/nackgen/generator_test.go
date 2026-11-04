package nackgen

import (
	"slices"
	"testing"
	"time"

	"github.com/pion/rtcp"
)

func newTestGenerator(t *testing.T, opts ...Option) *Generator {
	t.Helper()
	i, err := NewFactory(opts...).NewInterceptor("")
	if err != nil {
		t.Fatal(err)
	}
	return i.(*Generator)
}

func requested(t *testing.T, pkts []rtcp.Packet) []uint16 {
	t.Helper()
	var out []uint16
	for _, pkt := range pkts {
		nack, ok := pkt.(*rtcp.TransportLayerNack)
		if !ok {
			t.Fatalf("unexpected packet %T", pkt)
		}
		for _, pair := range nack.Nacks {
			out = append(out, pair.PacketList()...)
		}
	}
	return out
}

func TestFactoryRejectsInvalidSize(t *testing.T) {
	for _, size := range []uint16{0, 32, 100, 513} {
		if _, err := NewFactory(Size(size)).NewInterceptor(""); err == nil {
			t.Fatalf("size %d accepted", size)
		}
	}
}

func TestCollectRequestsMissingAndHonoursRetryLimit(t *testing.T) {
	g := newTestGenerator(t, Size(64), MaxNacksPerPacket(2), Interval(time.Hour))
	log := newReceiveLog(g.size)
	g.receiveLogs[1] = log
	for _, seq := range []uint16{10, 11, 14, 16} {
		log.add(seq)
	}
	missing, filtered := make([]uint16, g.size), make([]uint16, g.size)
	for round := range 2 {
		if got := requested(t, g.collect(7, missing, filtered)); !slices.Equal(got, []uint16{12, 13, 15}) {
			t.Fatalf("round %d requested %v", round, got)
		}
	}
	if got := g.collect(7, missing, filtered); len(got) != 0 {
		t.Fatalf("retry limit exceeded: %v", requested(t, got))
	}
	// A recovered packet leaves the retry counters; new loss starts fresh.
	log.add(12)
	log.add(13)
	log.add(15)
	log.add(18)
	if got := requested(t, g.collect(7, missing, filtered)); !slices.Equal(got, []uint16{17}) {
		t.Fatalf("after recovery requested %v", got)
	}
	if counts := g.nackCountLogs[1]; len(counts) != 1 || counts[17] != 1 {
		t.Fatalf("stale retry counters retained: %v", counts)
	}
}

func TestReceiveLogLargeForwardJumpClearsRing(t *testing.T) {
	log := newReceiveLog(64)
	for seq := uint16(0); seq < 64; seq++ {
		log.add(seq)
	}
	log.add(30000)
	buf := make([]uint16, 64)
	got := log.missingSeqNumbers(buf)
	if len(got) != 63 || got[0] != 30000-63 || got[62] != 29999 {
		t.Fatalf("missing after jump = %d entries starting %v", len(got), got[:min(3, len(got))])
	}
}

// The full 16384-packet window with every packet missing must stay linear.
// Upstream's quadratic pruning took seconds per tick here.
func TestCollectFullWindowIsLinear(t *testing.T) {
	g := newTestGenerator(t, Size(16384), MaxNacksPerPacket(26), Interval(time.Hour))
	log := newReceiveLog(g.size)
	g.receiveLogs[1] = log
	log.add(0)
	log.add(16383)
	missing, filtered := make([]uint16, g.size), make([]uint16, g.size)
	start := time.Now()
	for range 30 {
		g.collect(7, missing, filtered)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("30 full-window ticks took %v", elapsed)
	}
	if len(g.nackCountLogs[1]) != 16382 {
		t.Fatalf("retry counters = %d", len(g.nackCountLogs[1]))
	}
}
