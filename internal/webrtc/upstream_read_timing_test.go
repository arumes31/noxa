package webrtc

import (
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

func TestUpstreamReadTimingExcludesBlockingReadAndPreservesAttributes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &upstreamFeedbackObserver{started: time.Now()}
		o.readTiming.calls.Store(63)
		attrs := interceptor.Attributes{"caller": 7}
		var retained interceptor.Attributes
		inner := o.BindRemoteStream(nil, interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
			retained = a
			time.Sleep(50 * time.Millisecond)
			b[0] = 42
			return 1, a, nil
		}))
		middle := interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
			n, result, err := inner.Read(b, a)
			time.Sleep(7 * time.Millisecond)
			return n, result, err
		})
		outer := (&upstreamReadTimingOuter{observer: &o.readTiming}).BindRemoteStream(nil, middle)
		b := make([]byte, 1)
		n, result, err := outer.Read(b, attrs)
		if n != 1 || err != nil || b[0] != 42 || !reflect.DeepEqual(result, attrs) || len(attrs) != 1 {
			t.Fatal("read or caller attributes changed")
		}
		if _, ok := retained[upstreamReadTimingKey{}]; !ok {
			t.Fatal("cleanup mutated retained inner attribute map")
		}
		d := o.readTimingSnapshot(time.Now())
		if d.CompletedSamples != 1 || d.DurationMaxUS != 7000 || d.DurationHistogram[4] != 1 {
			t.Fatalf("includes blocking read: %+v", d)
		}
		d.DurationHistogram[4] = 0
		if o.readTimingSnapshot(time.Now()).DurationHistogram[4] != 1 {
			t.Fatal("snapshot aliases counters")
		}
	})
}

func TestUpstreamReadTimingActualTWCCBlockedHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := &upstreamFeedbackRegistry{}
		_, factories, err := newEngineMediaObserved(false, VideoBounds{}, nil, registry)
		if err != nil {
			t.Fatal(err)
		}
		lease := registry.arm()
		chain, err := factories.Build("test")
		o := registry.take(lease, "test")
		registry.disarm(lease)
		if err != nil {
			t.Fatal(err)
		}
		if o == nil {
			t.Fatal("production observer missing")
		}
		entered, release := make(chan struct{}), make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
			_ = chain.Close()
		}()
		chain.BindRTCPWriter(interceptor.RTCPWriterFunc(func(_ []rtcp.Packet, _ interceptor.Attributes) (int, error) {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-release
			return 1, nil
		}))
		info := &interceptor.StreamInfo{SSRC: 7, ClockRate: 90000, MimeType: "video/H264", RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}
		seq := uint16(1)
		reader := chain.BindRemoteStream(info, interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
			packet := rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 7, SequenceNumber: seq}, Payload: []byte{9}}
			ext, _ := (&rtp.TransportCCExtension{TransportSequence: seq}).Marshal()
			if err := packet.SetExtension(3, ext); err != nil {
				return 0, a, err
			}
			seq++
			n, err := packet.MarshalTo(b)
			return n, a, err
		}))
		o.readTiming.calls.Store(63)
		if _, _, err = reader.Read(make([]byte, 1500), nil); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		<-entered
		o.readTiming.calls.Store(127)
		done := make(chan error, 1)
		go func() { _, _, readErr := reader.Read(make([]byte, 1500), nil); done <- readErr }()
		synctest.Wait()
		if o.readTimingSnapshot(time.Now()).CompletedSamples != 1 {
			t.Fatal("blocked handoff incorrectly completed")
		}
		time.Sleep(150 * time.Millisecond)
		close(release)
		released = true
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		d := o.readTimingSnapshot(time.Now())
		if d.CompletedSamples != 2 || d.DurationMaxUS != 150000 || d.DurationHistogram[6] != 1 {
			t.Fatalf("missed synchronous TWCC handoff: %+v", d)
		}
	})
}

func TestUpstreamReadTimingOverlappingSamplesCompleteOutOfOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &upstreamFeedbackObserver{started: time.Now()}
		a := interceptor.Attributes{"caller": "unchanged"}
		gate1, gate2 := make(chan struct{}), make(chan struct{})
		done := make(chan error, 2)
		var tokens [2]*upstreamReadTimingToken
		inner := o.BindRemoteStream(nil, interceptor.RTPReaderFunc(func(b []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			tokens[int(b[0])-1] = attrs[upstreamReadTimingKey{}].(*upstreamReadTimingToken)
			return 1, attrs, nil
		}))
		middle := interceptor.RTPReaderFunc(func(b []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			n, result, err := inner.Read(b, attrs)
			if b[0] == 1 {
				<-gate1
			} else {
				<-gate2
			}
			return n, result, err
		})
		outer := (&upstreamReadTimingOuter{observer: &o.readTiming}).BindRemoteStream(nil, middle)
		read := func(value byte) {
			n, result, err := outer.Read([]byte{value}, a)
			if err == nil && (n != 1 || !reflect.DeepEqual(result, a)) {
				err = errors.New("result changed")
			}
			done <- err
		}
		o.readTiming.calls.Store(63)
		go read(1)
		synctest.Wait()
		time.Sleep(10 * time.Millisecond)
		o.readTiming.calls.Store(127)
		go read(2)
		synctest.Wait()
		if tokens[0] == nil || tokens[1] == nil || tokens[0] == tokens[1] || len(a) != 1 {
			t.Fatal("shared invocation state")
		}
		time.Sleep(20 * time.Millisecond)
		close(gate2)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		close(gate1)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		d := o.readTimingSnapshot(time.Now())
		if d.CompletedSamples != 2 || d.DurationTotalUS != 100000 || d.DurationMaxUS != 80000 || d.DurationHistogram[4] != 1 || d.DurationHistogram[5] != 1 {
			t.Fatalf("crossed invocation timestamps: %+v", d)
		}
	})
}

func TestUpstreamReadTimingUnknownErrorsSamplingAndClose(t *testing.T) {
	o := &upstreamFeedbackObserver{started: time.Now()}
	outer := &upstreamReadTimingOuter{observer: &o.readTiming}
	want := errors.New("reader error")
	errorReader := outer.BindRemoteStream(nil, interceptor.RTPReaderFunc(func(_ []byte, _ interceptor.Attributes) (int, interceptor.Attributes, error) { return 3, nil, want }))
	for range 128 {
		n, a, err := errorReader.Read(nil, nil)
		if n != 3 || a != nil || err != want {
			t.Fatal("read result changed")
		}
	}
	d := o.readTimingSnapshot(time.Now())
	if d.ReadCalls != 128 || d.SampledCalls != 2 || d.ReadErrors != 2 || d.CompletedSamples != 0 {
		t.Fatalf("sampling: %+v", d)
	}
	o.readTiming.calls.Store(191)
	missing := outer.BindRemoteStream(nil, interceptor.RTPReaderFunc(func(_ []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) { return 1, a, nil }))
	if _, a, err := missing.Read(nil, nil); err != nil || len(a) != 0 {
		t.Fatal("private token escaped")
	}
	if o.readTimingSnapshot(time.Now()).MissingInner != 1 {
		t.Fatal("missing edge manufactured zero duration")
	}
	o.readTiming.calls.Store(255)
	attrs := interceptor.Attributes{upstreamReadTimingKey{}: "foreign"}
	if _, a, err := missing.Read(nil, attrs); err != nil || !reflect.DeepEqual(a, attrs) {
		t.Fatal("collision mutated caller")
	}
	if o.readTimingSnapshot(time.Now()).InvalidToken != 1 {
		t.Fatal("collision not reported")
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	before := o.readTimingSnapshot(time.Now())
	_, _, _ = missing.Read(nil, nil)
	after := o.readTimingSnapshot(time.Now())
	if after.ReadCalls != before.ReadCalls || !after.Closed {
		t.Fatal("closed observer kept recording")
	}
	if (*upstreamFeedbackObserver)(nil).readTimingSnapshot(time.Now()) != nil {
		t.Fatal("missing observer fabricated data")
	}
}

func TestUpstreamReadTimingRepeatedInnerAndForeignTokenFailUnknown(t *testing.T) {
	o := &upstreamFeedbackObserver{started: time.Now()}
	token := &upstreamReadTimingToken{owner: &o.readTiming}
	inner := o.BindRemoteStream(nil, interceptor.RTPReaderFunc(func(_ []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) { return 1, a, nil }))
	a := interceptor.Attributes{upstreamReadTimingKey{}: token}
	_, _, _ = inner.Read(nil, a)
	_, _, _ = inner.Read(nil, a)
	o.readTiming.record(token, time.Now(), false, false)
	foreign := &upstreamReadTimingToken{owner: &upstreamReadTimingObserver{}}
	_, _, _ = inner.Read(nil, interceptor.Attributes{upstreamReadTimingKey{}: foreign})
	o.readTiming.record(foreign, time.Now(), false, false)
	d := o.readTimingSnapshot(time.Now())
	if d.CompletedSamples != 0 || d.InvalidToken != 2 || d.MissingInner != 0 {
		t.Fatalf("invalid observations: %+v", d)
	}
}

func TestUpstreamReadTimingFactoryRequiresExactConstruction(t *testing.T) {
	r := &upstreamFeedbackRegistry{}
	f := upstreamReadTimingOuterFactory{registry: r}
	if v, _ := f.NewInterceptor("id"); reflect.TypeOf(v) != reflect.TypeOf(&interceptor.NoOp{}) {
		t.Fatal("outside handoff retained observer")
	}
	lease := r.arm()
	defer r.disarm(lease)
	inner, err := (upstreamFeedbackFactory{registry: r}).NewInterceptor("id")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.NewInterceptor("other"); reflect.TypeOf(v) != reflect.TypeOf(&interceptor.NoOp{}) {
		t.Fatal("wrong peer observed")
	}
	v, err := f.NewInterceptor("id")
	if err != nil {
		t.Fatal(err)
	}
	if v.(*upstreamReadTimingOuter).observer != &inner.(*upstreamFeedbackObserver).readTiming {
		t.Fatal("wrong lifecycle observer")
	}
	_ = inner.Close()
	if v, _ := f.NewInterceptor("id"); reflect.TypeOf(v) != reflect.TypeOf(&interceptor.NoOp{}) {
		t.Fatal("closed pending observer retained")
	}
}

// Synthetic read overhead only; excludes SRTP, network and normal interceptors.
func BenchmarkUpstreamReadTiming(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "reference"
		if enabled {
			name = "sample_every_64"
		}
		b.Run(name, func(b *testing.B) {
			var reader interceptor.RTPReader = interceptor.RTPReaderFunc(func(_ []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) { return 1, a, nil })
			if enabled {
				o := &upstreamFeedbackObserver{started: time.Now()}
				reader = (&upstreamReadTimingOuter{observer: &o.readTiming}).BindRemoteStream(nil, o.BindRemoteStream(nil, reader))
			}
			buffer := []byte{1}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = reader.Read(buffer, nil)
			}
		})
	}
}
