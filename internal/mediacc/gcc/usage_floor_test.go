package gcc

import (
	"testing"
	"time"

	"github.com/pion/logging"
	"noxa/internal/mediacc/cc"
)

func floorController(now *time.Time) *rateController {
	c := newRateController(func() time.Time { return *now }, 2_000_000, 5000, 50_000_000, func(DelayStats) {})
	c.updateRTT(40 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal}) // initialize
	return c
}

func TestUsageFloorRestoresCleanDeliveredThroughput(t *testing.T) {
	now := time.Unix(100, 0)
	c := floorController(&now)
	// A relay pacer backlogged behind a recovered path delivers 1.5x target.
	c.onReceivedRate(3_000_000)
	c.onDeliveryRatio(1)
	c.lastUpdate = now
	now = now.Add(50 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.target != 3_000_000 {
		t.Fatalf("delivered throughput did not bound the estimate: %d", c.target)
	}
}

func TestUsageFloorWaitsForCleanHoldoffAfterOveruse(t *testing.T) {
	now := time.Unix(100, 0)
	c := floorController(&now)
	c.onReceivedRate(2_000_000)
	c.onDeliveryRatio(1)
	now = now.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageOver})
	decreased := c.target
	if decreased >= 2_000_000 {
		t.Fatalf("overuse did not decrease: %d", decreased)
	}
	c.onReceivedRate(3_000_000)
	now = now.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal}) // hold
	now = now.Add(100 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal}) // increase, inside hold-off
	if c.target > decreased+decreased/10 {
		t.Fatalf("floor applied inside the hold-off: %d", c.target)
	}
	now = now.Add(time.Second)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.target != 3_000_000 {
		t.Fatalf("floor not applied after a clean hold-off: %d", c.target)
	}
}

func TestUsageFloorRequiresPathToKeepPaceWithSender(t *testing.T) {
	now := time.Unix(100, 0)
	c := floorController(&now)
	// Receive spans 20% longer than send spans: a queue is growing.
	c.onReceivedRate(3_000_000)
	c.onDeliveryRatio(0.8)
	c.lastUpdate = now
	now = now.Add(50 * time.Millisecond)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.target >= 3_000_000 {
		t.Fatalf("throughput of a falling-behind path became a floor: %d", c.target)
	}
}

func TestUsageFloorBacksOffWhenRaisesEndInCongestion(t *testing.T) {
	now := time.Unix(100, 0)
	c := floorController(&now)
	c.onDeliveryRatio(1)
	holdoffs := []time.Duration{}
	for range 5 {
		c.onReceivedRate(c.target * 3 / 2)
		now = now.Add(c.usageFloorHoldoff())
		c.onDelayStats(DelayStats{Usage: usageNormal})
		if c.lastFloorRaise.IsZero() {
			t.Fatal("floor did not raise")
		}
		c.setPacedBitrate(c.target) // SendSideBWE paces at the combined target
		now = now.Add(300 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageOver})
		holdoffs = append(holdoffs, c.usageFloorHoldoff())
		now = now.Add(100 * time.Millisecond)
		c.onDelayStats(DelayStats{Usage: usageNormal})
	}
	for i := 1; i < len(holdoffs); i++ {
		if holdoffs[i] < holdoffs[i-1] {
			t.Fatalf("hold-off shrank while raises kept overshooting: %v", holdoffs)
		}
	}
	if holdoffs[len(holdoffs)-1] != usageFloorMaxHoldoff {
		t.Fatalf("hold-off did not reach its ceiling: %v", holdoffs)
	}
	// A long clean period restores the normal hold-off.
	now = now.Add(usageFloorBackoffReset + time.Second)
	c.onDelayStats(DelayStats{Usage: usageNormal})
	if c.usageFloorHoldoff() != time.Second {
		t.Fatalf("hold-off not reset after a clean period: %v", c.usageFloorHoldoff())
	}
}

func TestLossEstimateAdoptsDeliveredRateOnlyWithoutLoss(t *testing.T) {
	now := time.Unix(100, 0)
	e := newLossBasedBWE(1_000_000, func() time.Time { return now }, logging.NewDefaultLoggerFactory())
	e.raiseToDeliveredRate(3_000_000)
	if got := e.getEstimate(10_000_000).TargetBitrate; got != 3_000_000 {
		t.Fatalf("clean delivered rate not adopted: %d", got)
	}
	e.averageLoss = 0.05
	e.raiseToDeliveredRate(6_000_000)
	if got := e.getEstimate(10_000_000).TargetBitrate; got != 3_000_000 {
		t.Fatalf("lossy path adopted delivered rate: %d", got)
	}
}

func TestDeliveryRatioComparesSendAndReceiveSpans(t *testing.T) {
	base := time.Unix(100, 0)
	rc := newRateCalculator(500 * time.Millisecond)
	acks := make([]cc.Acknowledgment, 50)
	for i := range acks {
		sent := base.Add(time.Duration(i) * 10 * time.Millisecond)
		acks[i] = cc.Acknowledgment{Size: 1200, Departure: sent, Arrival: sent.Add(20*time.Millisecond + time.Duration(i)*2*time.Millisecond)}
	}
	rc.add(acks, func(int) {})
	// Each 10 ms send interval arrives over 12 ms.
	if got := rc.deliveryRatio(); got < 0.82 || got > 0.85 {
		t.Fatalf("delivery ratio %.3f, want 10/12", got)
	}
	short := newRateCalculator(500 * time.Millisecond)
	short.add(acks[:3], func(int) {})
	if short.deliveryRatio() != 0 {
		t.Fatal("a too-short window was judged")
	}
}
