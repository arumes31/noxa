package gcc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"noxa/internal/mediacc/cc"
)

func videoProbeEstimator(t *testing.T) *SendSideBWE {
	t.Helper()
	e, err := NewSendSideBWE(SendSideBWEPacer(NewNoOpPacer()), SendSideBWEInitialBitrate(2_000_000), SendSideBWEMaxBitrate(12_000_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestVideoProbeIsOneShotAndExpiresWithoutFeedback(t *testing.T) {
	e := videoProbeEstimator(t)
	if !e.StartVideoProbe(50_000_000) || e.GetTargetBitrate() != 12_000_000 {
		t.Fatal("probe did not respect the configured ceiling")
	}
	if e.StartVideoProbe(9_000_000) {
		t.Fatal("second video stream restarted the probe")
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.GetTargetBitrate() != 2_000_000 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.GetTargetBitrate() != 2_000_000 || e.StartVideoProbe(10_000_000) {
		t.Fatal("unacknowledged capacity persisted or probe restarted")
	}
}

func TestVideoProbeUnknownDemandConsumesAttemptWithoutRaising(t *testing.T) {
	for _, demand := range []int{0, -1, 1_000_000} {
		e := videoProbeEstimator(t)
		if e.StartVideoProbe(demand) || e.StartVideoProbe(10_000_000) || e.GetTargetBitrate() != 2_000_000 {
			t.Fatalf("unknown/sub-target demand %d raised pacing or allowed a retry", demand)
		}
	}
}

func probeAcknowledgements(start time.Time) []cc.Acknowledgment {
	acks := make([]cc.Acknowledgment, 601)
	for i := range acks {
		acks[i] = cc.Acknowledgment{SequenceNumber: uint16(i), Size: 1000,
			Departure: start.Add(10*time.Millisecond + time.Duration(i)*time.Millisecond),
			Arrival:   time.Unix(100, 0).Add(time.Duration(i) * time.Millisecond)}
	}
	return acks
}

func TestVideoProbeRequiresFreshSustainedUniqueAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alter   func([]cc.Acknowledgment, time.Time) []cc.Acknowledgment
		promote bool
	}{
		{"measured capacity", func(acks []cc.Acknowledgment, _ time.Time) []cc.Acknowledgment { return acks }, true},
		{"compressed arrival burst", func(acks []cc.Acknowledgment, _ time.Time) []cc.Acknowledgment {
			for i := range acks {
				acks[i].Arrival = acks[0].Arrival.Add(time.Duration(i) * time.Microsecond)
			}
			return acks
		}, false},
		{"less than meaningful span", func(acks []cc.Acknowledgment, _ time.Time) []cc.Acknowledgment { return acks[:100] }, false},
		{"old history", func(acks []cc.Acknowledgment, _ time.Time) []cc.Acknowledgment {
			for i := range acks {
				acks[i].Departure = acks[i].Departure.Add(-time.Second)
			}
			return acks
		}, false},
		{"duplicate report", func(acks []cc.Acknowledgment, _ time.Time) []cc.Acknowledgment {
			for i := range acks {
				acks[i] = acks[0]
			}
			return acks
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := videoProbeEstimator(t)
			if !e.StartVideoProbe(10_000_000) {
				t.Fatal("probe did not start")
			}
			e.lock.Lock()
			start := e.videoProbe.started
			e.lock.Unlock()
			acks := tc.alter(probeAcknowledgements(start), start)
			e.observeVideoProbe(acks, start.Add(800*time.Millisecond))
			// Repeated feedback must not inflate measured throughput.
			e.observeVideoProbe(acks, start.Add(850*time.Millisecond))
			e.finishVideoProbe(start.Add(time.Second))
			got := e.GetTargetBitrate()
			if tc.promote && got != 8_000_000 || !tc.promote && got != 2_000_000 {
				t.Fatalf("post-probe target = %d", got)
			}
			if tc.promote {
				e.onDelayUpdate(DelayStats{generation: e.controllerGeneration, TargetBitrate: 6_000_000, Usage: usageOver, State: stateDecrease})
				if e.GetTargetBitrate() > 6_000_000 {
					t.Fatal("confirmed rate became a floor against congestion")
				}
			}
		})
	}
}

func TestVideoProbeAbortsOnLossOrDelayDecrease(t *testing.T) {
	for _, loss := range []bool{false, true} {
		e := videoProbeEstimator(t)
		e.StartVideoProbe(10_000_000)
		if loss {
			e.lossController.updateLossEstimate([]cc.Acknowledgment{{}, {}, {Arrival: time.Now()}})
			e.observeVideoProbe(nil, time.Now())
		} else {
			e.onDelayUpdate(DelayStats{generation: e.controllerGeneration, TargetBitrate: 8_500_000, Usage: usageOver, State: stateDecrease})
		}
		if e.GetTargetBitrate() > 2_000_000 || e.StartVideoProbe(10_000_000) {
			t.Fatal("congestion did not end the probe conservatively")
		}
	}
}

func TestVideoProbeAbortsThroughRTCPFeedback(t *testing.T) {
	e := videoProbeEstimator(t)
	e.StartVideoProbe(8_000_000)
	for sequence := uint16(0); sequence < 3; sequence++ {
		header := rtp.Header{Version: 2, SSRC: 123, SequenceNumber: sequence}
		extension := rtp.TransportCCExtension{TransportSequence: sequence}
		encoded, err := extension.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := header.SetExtension(1, encoded); err != nil {
			t.Fatal(err)
		}
		if err := e.feedbackAdapter.OnSent(time.Now(), &header, 1000,
			interceptor.Attributes{cc.TwccExtensionAttributesKey: uint8(1)}); err != nil {
			t.Fatal(err)
		}
	}
	feedback := &rtcp.TransportLayerCC{PacketStatusCount: 3, ReferenceTime: 1,
		PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{
			PacketStatusSymbol: rtcp.TypeTCCPacketNotReceived, RunLength: 3,
		}}}
	if err := e.WriteRTCP([]rtcp.Packet{feedback}, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.GetTargetBitrate(); got != 2_000_000 {
		t.Fatalf("real loss feedback left the startup rate active: %d", got)
	}
}

func TestVideoProbeRejectsBlockedOldControllerCallback(t *testing.T) {
	e := videoProbeEstimator(t)
	controller := e.delayController.rateController
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	writer := controller.dsWriter
	controller.dsWriter = func(stats DelayStats) {
		close(entered)
		<-release
		writer(stats)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	controller.onDelayStats(DelayStats{Usage: usageNormal})
	go func() {
		defer close(done)
		controller.onDelayStats(DelayStats{Usage: usageOver})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("controller did not reach the blocked callback")
	}
	if !e.StartVideoProbe(8_000_000) {
		t.Fatal("probe did not start while an old callback was pending")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller callback did not complete")
	}
	if e.GetTargetBitrate() != 8_000_000 {
		t.Fatal("old congestion decision canceled the new probe")
	}
	e.lock.Lock()
	start, generation := e.videoProbe.started, e.controllerGeneration
	e.lock.Unlock()
	e.observeVideoProbe(probeAcknowledgements(start), start.Add(800*time.Millisecond))
	e.finishVideoProbe(start.Add(time.Second))
	e.onDelayUpdate(DelayStats{generation: generation, TargetBitrate: 1_000_000, State: stateDecrease})
	if e.GetTargetBitrate() != 8_000_000 {
		t.Fatal("old probe callback overwrote confirmed measured capacity")
	}
}

func TestVideoProbeConcurrentControllerStateAndClose(t *testing.T) {
	e, err := NewSendSideBWE(SendSideBWEPacer(NewNoOpPacer()), SendSideBWEInitialBitrate(2_000_000))
	if err != nil {
		t.Fatal(err)
	}
	e.StartVideoProbe(8_000_000)
	e.lock.Lock()
	start := e.videoProbe.started
	e.lock.Unlock()
	ready := make(chan struct{})
	closeResult := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		<-ready
		for range 100 {
			e.StartVideoProbe(8_000_000)
			e.GetStats()
		}
	})
	workers.Go(func() {
		<-ready
		for range 100 {
			e.delayController.onReceivedRate(3_000_000)
			e.delayController.onDelayStats(DelayStats{Usage: usageNormal})
		}
	})
	workers.Go(func() {
		<-ready
		e.finishVideoProbe(start.Add(time.Second))
	})
	workers.Go(func() {
		<-ready
		closeResult <- e.Close()
	})
	close(ready)
	workers.Wait()
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if e.StartVideoProbe(10_000_000) {
		t.Fatal("closed estimator accepted a probe")
	}
}
