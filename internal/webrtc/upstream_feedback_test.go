package webrtc

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

func TestUpstreamFeedbackPassThroughAndSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observer := &upstreamFeedbackObserver{started: time.Now()}
		sentinel := errors.New("synthetic write error")
		feedback := &rtcp.TransportLayerCC{PacketStatusCount: 7}
		packets := []rtcp.Packet{&rtcp.PictureLossIndication{}, feedback, &rtcp.TransportLayerCC{PacketStatusCount: 3}}
		attrs := interceptor.Attributes{"sentinel": true}
		writes := 0
		writer := observer.BindRTCPWriter(interceptor.RTCPWriterFunc(func(got []rtcp.Packet, a interceptor.Attributes) (int, error) {
			writes++
			if &got[0] != &packets[0] || got[1] != feedback || a["sentinel"] != true {
				t.Error("write inputs changed")
			}
			a["writer mutation"] = true
			time.Sleep(5 * time.Millisecond)
			return 123, sentinel
		}))
		if n, err := writer.Write(packets, attrs); n != 123 || err != sentinel || attrs["writer mutation"] != true {
			t.Fatal("write result or attrs identity changed")
		}
		first := observer.snapshot(time.Now())
		if first.Attempts != 1 || first.CompletedWrites != 1 || first.Reports != 2 || first.ReportedPackets != 10 || first.WriteErrors != 1 || first.DurationTotalUS != 5000 || first.DurationMaxUS != 5000 || first.DurationHistogram[3] != 1 || first.LastAttemptGapUS != nil || first.InFlight != 0 || first.InFlightOldestAgeUS != nil {
			t.Fatalf("incorrect completed write: %+v", first)
		}
		time.Sleep(15 * time.Millisecond)
		if _, err := writer.Write(packets, attrs); err != sentinel {
			t.Fatal(err)
		}
		second := observer.snapshot(time.Now())
		if second.LastAttemptGapUS == nil || *second.LastAttemptGapUS != 20000 || second.MaxAttemptGapUS != 20000 || second.SampleMS != 25 {
			t.Fatalf("incorrect gap/lifetime: %+v", second)
		}
		*second.LastAttemptGapUS = -99
		second.DurationHistogram[3] = 99
		if got := observer.snapshot(time.Now()); *got.LastAttemptGapUS != 20000 || got.DurationHistogram[3] != 2 || first.CompletedWrites != 1 {
			t.Fatal("snapshot aliases live or old state")
		}
		noFeedback := observer.BindRTCPWriter(interceptor.RTCPWriterFunc(func(got []rtcp.Packet, a interceptor.Attributes) (int, error) {
			if a != nil {
				t.Error("nil attrs changed")
			}
			return len(got), sentinel
		}))
		if n, err := noFeedback.Write([]rtcp.Packet{&rtcp.PictureLossIndication{}}, nil); n != 1 || err != sentinel {
			t.Fatal("non-TWCC pass-through changed")
		}
		if observer.snapshot(time.Now()).Attempts != 2 || writes != 2 {
			t.Fatal("non-TWCC write counted")
		}
		if err := observer.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(packets, attrs); err != sentinel {
			t.Fatal("closed observer changed writer result")
		}
		if got := observer.snapshot(time.Now()); got.Attempts != 2 || !got.Closed {
			t.Fatal("closed observer still records")
		}
	})
}

func TestUpstreamFeedbackBoundedConcurrentWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observer := &upstreamFeedbackObserver{started: time.Now()}
		entered := make(chan struct{}, upstreamFeedbackSlots+2)
		release := make(chan struct{})
		var once sync.Once
		defer once.Do(func() { close(release) })
		writer := observer.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) {
			entered <- struct{}{}
			<-release
			return 7, nil
		}))
		var writes sync.WaitGroup
		for range upstreamFeedbackSlots + 2 {
			writes.Add(1)
			go func() {
				defer writes.Done()
				_, _ = writer.Write([]rtcp.Packet{&rtcp.TransportLayerCC{PacketStatusCount: 1}}, nil)
			}()
		}
		for range upstreamFeedbackSlots + 2 {
			<-entered
		}
		time.Sleep(200 * time.Millisecond)
		got := observer.snapshot(time.Now())
		if got.InFlight != 18 || got.InFlightOverflow != 2 || !got.InFlightAgeUnknown || got.InFlightOldestAgeUS != nil || got.CompletedWrites != 0 {
			t.Fatalf("overflow presented false precision: %+v", got)
		}
		// Snapshot and Close must complete while every real write is blocked.
		if err := observer.Close(); err != nil {
			t.Fatal(err)
		}
		once.Do(func() { close(release) })
		writes.Wait()
		got = observer.snapshot(time.Now())
		if got.InFlight != 0 || got.InFlightAgeUnknown || got.InFlightOldestAgeUS != nil || got.CompletedWrites != 18 || got.DurationHistogram[6] != 18 || got.DurationTotalUS != 3_600_000 {
			t.Fatalf("in-flight cleanup failed: %+v", got)
		}
	})
}

func TestUpstreamFeedbackActualDefaultTWCCWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := &upstreamFeedbackRegistry{}
		_, factories, err := newEngineMediaObserved(false, VideoBounds{}, nil, registry)
		if err != nil {
			t.Fatal(err)
		}
		lease := registry.arm()
		chain, err := factories.Build("actual-pion-chain")
		if err != nil {
			registry.disarm(lease)
			t.Fatal(err)
		}
		observer := registry.take(lease, "actual-pion-chain")
		registry.disarm(lease)
		if observer == nil {
			t.Fatal("observer handoff missing")
		}
		entered, release := make(chan struct{}, 1), make(chan struct{})
		var once sync.Once
		defer func() { once.Do(func() { close(release) }); _ = chain.Close() }()
		chain.BindRTCPWriter(interceptor.RTCPWriterFunc(func(pkts []rtcp.Packet, _ interceptor.Attributes) (int, error) {
			for _, p := range pkts {
				if _, ok := p.(*rtcp.TransportLayerCC); ok {
					entered <- struct{}{}
					<-release
				}
			}
			return len(pkts), nil
		}))
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: 1}, Payload: []byte{0x61, 1}}
		extension, err := (&rtp.TransportCCExtension{TransportSequence: 1}).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err = packet.SetExtension(3, extension); err != nil {
			t.Fatal(err)
		}
		raw, err := packet.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		reader := chain.BindRemoteStream(&interceptor.StreamInfo{SSRC: 42, ClockRate: 90000, MimeType: "video/H264", RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}, interceptor.RTPReaderFunc(func(out []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
			return copy(out, raw), a, nil
		}))
		if _, _, err = reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
		<-entered // Pion's actual100ms feedback timer advances in virtual time.
		time.Sleep(120 * time.Millisecond)
		got := observer.snapshot(time.Now())
		if got.Attempts != 1 || got.InFlight != 1 || got.InFlightOldestAgeUS == nil || *got.InFlightOldestAgeUS != 120000 || got.ReportedPackets != 1 {
			t.Fatalf("observer missed actual TWCC writer: %+v", got)
		}
		once.Do(func() { close(release) })
		synctest.Wait()
		got = observer.snapshot(time.Now())
		if got.CompletedWrites != 1 || got.InFlight != 0 || got.DurationTotalUS != 120000 {
			t.Fatalf("actual writer completion missing: %+v", got)
		}
	})
}

func TestUpstreamFeedbackRegistryOwnership(t *testing.T) {
	r := &upstreamFeedbackRegistry{}
	f := upstreamFeedbackFactory{registry: r}
	idle, err := f.NewInterceptor("unowned")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idle.(*interceptor.NoOp); !ok {
		t.Fatal("unarmed API retained observation state")
	}
	firstLease := r.arm()
	first, err := f.NewInterceptor("first")
	if err != nil {
		t.Fatal(err)
	}
	if r.take(firstLease, "wrong") != nil {
		t.Fatal("wrong Pion ID accepted")
	}
	r.disarm(firstLease) // Includes a failed constructor with no usable PC ID.
	secondLease := r.arm()
	second, err := f.NewInterceptor("second")
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if r.take(secondLease, "second") != second {
		t.Fatal("stale Close erased new handoff")
	}
	r.disarm(firstLease)
	if r.lease != secondLease {
		t.Fatal("stale disarm erased another construction")
	}
	r.disarm(secondLease)
	lease := r.arm()
	_, err = f.NewInterceptor("outer")
	if err != nil {
		t.Fatal(err)
	}
	nested := r.arm()
	r.disarm(nested)
	if nested != nil || r.take(lease, "outer") != nil {
		t.Fatal("reentrant construction gave false attribution")
	}
	r.disarm(lease)
	if r.pending != nil || r.lease != nil {
		t.Fatal("handoff leaked")
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamFeedbackFailedPionConstructionAndPeerLifecycle(t *testing.T) {
	registry := &upstreamFeedbackRegistry{}
	factories := &interceptor.Registry{}
	factories.Add(upstreamFeedbackFactory{registry: registry})
	engine := &Engine{api: pion.NewAPI(pion.WithInterceptorRegistry(factories)), upstreamFeedback: registry, logger: zap.NewNop(), peers: map[string]*PeerConnectionWrapper{}, iceServers: []pion.ICEServer{{URLs: []string{"invalid-url"}}}}
	if _, err := engine.NewPeerConnection("failed"); err == nil {
		t.Fatal("invalid Pion constructor unexpectedly succeeded")
	}
	if registry.pending != nil || registry.lease != nil {
		t.Fatal("failed constructor retained handoff")
	}
	actual, err := New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = actual.Close() }()
	peer, err := actual.NewPeerConnection("publisher")
	if err != nil {
		t.Fatal(err)
	}
	if peer.upstreamFeedback == nil || actual.upstreamFeedback.pending != nil || actual.upstreamFeedback.lease != nil {
		t.Fatal("actual Pion ID not transferred")
	}
	if err = peer.Close(); err != nil {
		t.Fatal(err)
	}
	if !peer.upstreamFeedback.snapshot(time.Now()).Closed {
		t.Fatal("Pion Close did not retire observer")
	}
}

func BenchmarkUpstreamFeedbackWrite(b *testing.B) {
	packets := []rtcp.Packet{&rtcp.TransportLayerCC{PacketStatusCount: 100}}
	sink := interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) { return 1, nil })
	for _, observed := range []bool{false, true} {
		name := "reference"
		var writer interceptor.RTCPWriter = sink
		if observed {
			name = "observed"
			writer = (&upstreamFeedbackObserver{started: time.Now()}).BindRTCPWriter(sink)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = writer.Write(packets, nil)
			}
		})
	}
}

// Channel gates are durably blocking in synctest; mutex contention is not.
func TestUpstreamFeedbackConcurrentAttemptTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observer := &upstreamFeedbackObserver{started: time.Now()}
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan struct{})
		var once sync.Once
		defer once.Do(func() { close(release) })
		first := observer.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) { close(entered); <-release; return 1, nil }))
		second := observer.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) {
			time.Sleep(2 * time.Millisecond)
			return 1, nil
		}))
		packets := []rtcp.Packet{&rtcp.TransportLayerCC{PacketStatusCount: 1}}
		go func() { _, _ = first.Write(packets, nil); close(done) }()
		<-entered
		time.Sleep(100 * time.Millisecond)
		if _, err := second.Write(packets, nil); err != nil {
			t.Fatal(err)
		}
		got := observer.snapshot(time.Now())
		if got.LastAttemptGapUS == nil || *got.LastAttemptGapUS != 100000 || got.LastAttemptAgeUS == nil || *got.LastAttemptAgeUS != 2000 || got.DurationTotalUS != 2000 || got.DurationMaxUS != 2000 || got.InFlightOldestAgeUS == nil || *got.InFlightOldestAgeUS != 102000 {
			t.Fatalf("overlapping writes distorted attempt ordering or completion duration: %+v", got)
		}
		once.Do(func() { close(release) })
		<-done
		got = observer.snapshot(time.Now())
		if got.InFlight != 0 || got.DurationTotalUS != 104000 || got.DurationMaxUS != 102000 || got.CompletedWrites != 2 || *got.LastAttemptGapUS != 100000 {
			t.Fatalf("older write completion changed latest attempt gap: %+v", got)
		}
	})
}
