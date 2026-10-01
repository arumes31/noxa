package gcc

import (
	"testing"
	"time"

	"noxa/internal/mediacc/cc"
)

func TestArrivalGroupsIgnoreInitialLoss(t *testing.T) {
	base := time.Unix(100, 0)
	input := make(chan []cc.Acknowledgment, 1)
	input <- []cc.Acknowledgment{
		{Departure: base},
		{Departure: base.Add(10 * time.Millisecond), Arrival: base.Add(60 * time.Millisecond)},
		{Departure: base.Add(20 * time.Millisecond), Arrival: base.Add(70 * time.Millisecond)},
	}
	close(input)
	var groups []arrivalGroup
	newArrivalGroupAccumulator().run(input, func(group arrivalGroup) { groups = append(groups, group) })
	if len(groups) != 1 || groups[0].arrival.IsZero() {
		t.Fatalf("loss created a synthetic arrival group: %+v", groups)
	}
}

func TestUnequalBurstWidthsDoNotManufactureQueueDelay(t *testing.T) {
	base := time.Unix(100, 0)
	ack := func(ms int) cc.Acknowledgment {
		sent := base.Add(time.Duration(ms) * time.Millisecond)
		return cc.Acknowledgment{Departure: sent, Arrival: sent.Add(50 * time.Millisecond)}
	}
	a := newArrivalGroup(ack(0))
	b := newArrivalGroup(ack(10))
	b.add(ack(14))
	if got := interGroupDelayVariation(a, b); got != 0 {
		t.Fatalf("constant 50ms path acquired %v queue delay", got)
	}
	if got := interDepartureTimePkt(b, ack(16)); got != 6*time.Millisecond {
		t.Fatalf("group boundary moved with the last packet: %v", got)
	}
}

func TestArrivalBurstCannotGrowBeyondOneHundredMilliseconds(t *testing.T) {
	base := time.Unix(100, 0)
	in := make(chan []cc.Acknowledgment, 1)
	acks := make([]cc.Acknowledgment, 200)
	for i := range acks {
		acks[i] = cc.Acknowledgment{Departure: base.Add(time.Duration(i) * 6 * time.Millisecond), Arrival: base.Add(time.Second + time.Duration(i)*4*time.Millisecond)}
	}
	in <- acks
	close(in)
	groups := 0
	newArrivalGroupAccumulator().run(in, func(group arrivalGroup) {
		groups++
		if duration := group.arrival.Sub(group.packets[0].Arrival); duration >= 100*time.Millisecond {
			t.Errorf("unbounded arrival burst: %v", duration)
		}
	})
	if groups < 2 {
		t.Fatal("continuous traffic never closed a burst group")
	}
}

func TestArrivalGroupKeepsEqualAndReorderedDepartureTimes(t *testing.T) {
	base := time.Unix(100, 0)
	in := make(chan []cc.Acknowledgment, 1)
	acks := []cc.Acknowledgment{}
	for i, ms := range []int{0, 0, 4, 2, 20} {
		acks = append(acks, cc.Acknowledgment{Departure: base.Add(time.Duration(ms) * time.Millisecond), Arrival: base.Add(time.Second + time.Duration(i)*6*time.Millisecond)})
	}
	in <- acks
	close(in)
	groups := []arrivalGroup{}
	newArrivalGroupAccumulator().run(in, func(group arrivalGroup) { groups = append(groups, group) })
	if len(groups) != 1 || len(groups[0].packets) != 4 || !groups[0].departure.Equal(base.Add(4*time.Millisecond)) {
		t.Fatalf("lost acknowledgements or latest departure: %+v", groups)
	}
}

func TestConstantFourMillisecondPacketTrainClosesGroups(t *testing.T) {
	base := time.Unix(100, 0)
	in := make(chan []cc.Acknowledgment, 1)
	acks := make([]cc.Acknowledgment, 100)
	for i := range acks {
		sent := base.Add(time.Duration(i) * 4 * time.Millisecond)
		acks[i] = cc.Acknowledgment{Departure: sent, Arrival: sent.Add(50 * time.Millisecond)}
	}
	in <- acks
	close(in)
	groups := 0
	newArrivalGroupAccumulator().run(in, func(group arrivalGroup) {
		groups++
		if len(group.packets) != 2 {
			t.Errorf("5ms departure boundary ignored: %d packets", len(group.packets))
		}
	})
	if groups != 49 {
		t.Fatalf("constant traffic stalled the estimator: %d groups", groups)
	}
}
