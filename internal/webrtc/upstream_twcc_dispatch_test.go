package webrtc

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

func dispatchTestReport(sequence uint16) *rtcp.TransportLayerCC {
	recorder := twcc.NewRecorder(42)
	recorder.Record(9, sequence, 10000)
	return recorder.BuildFeedbackPacket()[0].(*rtcp.TransportLayerCC)
}

func TestUpstreamTWCCDispatchBoundedFIFOAndCopies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newUpstreamTWCCDispatch()
		entered, release := make(chan struct{}), make(chan struct{})
		reports := make(chan *rtcp.TransportLayerCC, 8)
		writer := d.BindRTCPWriter(interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
			p := packets[0].(*rtcp.TransportLayerCC)
			if p.BaseSequenceNumber == 1 {
				close(entered)
				<-release
			}
			reports <- p
			return len(packets), nil
		}))
		if _, err := writer.Write([]rtcp.Packet{dispatchTestReport(1)}, nil); err != nil {
			t.Fatal(err)
		}
		<-entered
		for seq := uint16(2); seq <= 6; seq++ {
			p := dispatchTestReport(seq)
			if _, err := writer.Write([]rtcp.Packet{p}, nil); err != nil {
				t.Fatal(err)
			}
			p.BaseSequenceNumber = 999
			p.RecvDeltas[0].Delta = 999999
		}
		state := d.snapshot(time.Now())
		if state.QueuedBatches != 4 || state.QueuedBytes <= 0 || state.QueuedBytes > upstreamTWCCDispatchBytes || state.DroppedOverflow != 1 || state.Accepted != 6 || state.Written != 0 {
			t.Fatalf("queue state: %+v", state)
		}
		close(release)
		synctest.Wait()
		for _, expected := range []uint16{1, 3, 4, 5, 6} {
			p := <-reports
			if p.BaseSequenceNumber != expected || p.RecvDeltas[0].Delta == 999999 {
				t.Fatalf("mutable or reordered report: %+v", p)
			}
		}
		if state = d.snapshot(time.Now()); state.Written != 5 || state.QueuedBatches != 0 || state.QueuedBytes != 0 || state.OldestAgeMS != nil {
			t.Fatalf("drained state: %+v", state)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUpstreamTWCCDispatchByteBoundAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newUpstreamTWCCDispatch()
		entered, release := make(chan struct{}), make(chan struct{})
		calls := 0
		writer := d.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) {
			calls++
			if calls == 1 {
				close(entered)
				<-release
			}
			return 0, errors.New("expected writer failure")
		}))
		_, _ = writer.Write([]rtcp.Packet{dispatchTestReport(1)}, nil)
		<-entered
		p := dispatchTestReport(2)
		large := make([]rtcp.Packet, upstreamTWCCDispatchBytes/(2*p.MarshalSize())+1)
		for i := range large {
			large[i] = p
		}
		for range 2 {
			if _, err := writer.Write(large, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got := d.snapshot(time.Now()); got.QueuedBatches != 1 || got.QueuedBytes > upstreamTWCCDispatchBytes || got.DroppedOverflow != 1 {
			t.Fatalf("byte bound: %+v", got)
		}
		oversized := append(append([]rtcp.Packet{}, large...), large...)
		if _, err := writer.Write(oversized, nil); err == nil {
			t.Fatal("oversize accepted")
		}
		if got := d.snapshot(time.Now()); got.RejectedOversize != 1 || got.Accepted != 3 {
			t.Fatalf("oversize state: %+v", got)
		}
		time.Sleep(249 * time.Millisecond)
		if got := d.snapshot(time.Now()); got.OldestAgeMS == nil || *got.OldestAgeMS != 249 {
			t.Fatalf("age: %+v", got)
		}
		time.Sleep(time.Millisecond)
		close(release)
		synctest.Wait()
		if got := d.snapshot(time.Now()); got.DroppedExpired != 1 || got.Written != 1 || got.WriteErrors != 1 || got.QueuedBatches != 0 {
			t.Fatalf("expiry/completion: %+v", got)
		}
		// Failure must not poison the next report or cause retry of the failed one.
		_, _ = writer.Write([]rtcp.Packet{dispatchTestReport(3)}, nil)
		synctest.Wait()
		if got := d.snapshot(time.Now()); got.Written != 2 || got.WriteErrors != 2 || calls != 2 {
			t.Fatalf("error recovery: %+v", got)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUpstreamTWCCDispatchCloseAndPassthrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newUpstreamTWCCDispatch()
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		passes := 0
		sentinel := errors.New("synchronous error")
		attributes := interceptor.Attributes{"reference": new(int)}
		nack := &rtcp.TransportLayerNack{MediaSSRC: 77}
		writer := d.BindRTCPWriter(interceptor.RTCPWriterFunc(func(p []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
			if len(p) != 1 || p[0] == nack || attrs != nil {
				passes++
				if attrs != nil && attrs["reference"] != attributes["reference"] {
					t.Error("attributes changed")
				}
				return 17, sentinel
			}
			once.Do(func() { close(entered); <-release })
			return 1, nil
		}))
		for _, tc := range []struct {
			p []rtcp.Packet
			a interceptor.Attributes
		}{
			{[]rtcp.Packet{nack}, nil},
			{[]rtcp.Packet{dispatchTestReport(1)}, attributes},
			{[]rtcp.Packet{dispatchTestReport(1), nack}, nil},
		} {
			if n, err := writer.Write(tc.p, tc.a); n != 17 || err != sentinel {
				t.Fatalf("passthrough changed: %d %v", n, err)
			}
		}
		if passes != 3 || d.snapshot(time.Now()).Accepted != 0 {
			t.Fatal("passthrough was queued")
		}
		_, _ = writer.Write([]rtcp.Packet{dispatchTestReport(2)}, nil)
		<-entered
		_, _ = writer.Write([]rtcp.Packet{dispatchTestReport(3)}, nil)
		closed := make(chan struct{})
		go func() { _ = d.Close(); close(closed) }()
		synctest.Wait()
		if got := d.snapshot(time.Now()); !got.Closed || got.QueuedBatches != 0 || got.QueuedBytes != 0 || got.DroppedClosed != 1 {
			t.Fatalf("close cleanup: %+v", got)
		}
		select {
		case <-closed:
			t.Fatal("Close did not wait for real writer")
		default:
		}
		if _, err := writer.Write([]rtcp.Packet{dispatchTestReport(4)}, nil); err != errUpstreamTWCCDispatchClosed {
			t.Fatal("closed admission")
		}
		if _, err := writer.Write([]rtcp.Packet{nack}, nil); err != errUpstreamTWCCDispatchClosed {
			t.Fatal("closed passthrough")
		}
		close(release)
		<-closed
		if passes != 3 || d.snapshot(time.Now()).Written != 1 {
			t.Fatal("pending feedback written after close")
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUpstreamTWCCDispatchKeepsPinnedRecorderReadsMoving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newUpstreamTWCCDispatch()
		ticker := &upstreamTWCCTestTicker{ticks: make(chan time.Time, 1)}
		factory, err := twcc.NewSenderInterceptor(twcc.SendTicker(func(time.Duration) twcc.Ticker { return ticker }))
		if err != nil {
			t.Fatal(err)
		}
		instance, err := factory.NewInterceptor("bounded-upstream")
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		reports := make(chan *rtcp.TransportLayerCC, 4)
		calls := 0
		instance.BindRTCPWriter(d.BindRTCPWriter(interceptor.RTCPWriterFunc(func(p []rtcp.Packet, _ interceptor.Attributes) (int, error) {
			calls++
			if calls == 1 {
				close(entered)
				<-release
			}
			reports <- p[0].(*rtcp.TransportLayerCC)
			return len(p), nil
		})))
		incoming := make(chan []byte, 3)
		info := &interceptor.StreamInfo{SSRC: 42, MimeType: "video/H264", RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}
		reader := instance.BindRemoteStream(info, interceptor.RTPReaderFunc(func(out []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
			return copy(out, <-incoming), attrs, nil
		}))
		read := func(sequence uint16) {
			t.Helper()
			p := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: sequence, Timestamp: 9000}, Payload: []byte{0x61, 1}}
			ext, e := (&rtp.TransportCCExtension{TransportSequence: sequence}).Marshal()
			if e != nil {
				t.Fatal(e)
			}
			if e = p.SetExtension(3, ext); e != nil {
				t.Fatal(e)
			}
			raw, e := p.Marshal()
			if e != nil {
				t.Fatal(e)
			}
			incoming <- raw
			if _, _, e = reader.Read(make([]byte, 1500), nil); e != nil {
				t.Fatal(e)
			}
		}
		read(1)
		ticker.ticks <- time.Now()
		<-entered
		read(2)
		time.Sleep(10 * time.Millisecond)
		read(3) // Must complete while the first real RTCP write remains blocked.
		ticker.ticks <- time.Now()
		synctest.Wait()
		time.Sleep(90 * time.Millisecond)
		if got := d.snapshot(time.Now()); got.Written != 0 || got.Accepted != 2 || got.QueuedBatches != 1 {
			t.Fatalf("recorder stopped: %+v", got)
		}
		close(release)
		synctest.Wait()
		first, next := <-reports, <-reports
		if first.BaseSequenceNumber != 1 || next.BaseSequenceNumber != 2 || next.PacketStatusCount != 2 || len(next.RecvDeltas) != 2 || next.RecvDeltas[1].Delta != 10000 {
			t.Fatalf("timestamps/coverage changed: first=%+v next=%+v", first, next)
		}
		if err := instance.Close(); err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUpstreamTWCCDispatchCloseBeforeBind(t *testing.T) {
	d := newUpstreamTWCCDispatch()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	writer := d.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) { called = true; return 1, nil }))
	if _, err := writer.Write([]rtcp.Packet{dispatchTestReport(1)}, nil); err != errUpstreamTWCCDispatchClosed || called {
		t.Fatal("unbound close allowed a writer")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamTWCCDispatchRejectsMalformedFeedback(t *testing.T) {
	d := newUpstreamTWCCDispatch()
	defer func() { _ = d.Close() }()
	writer := d.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) {
		t.Error("invalid feedback reached writer")
		return 0, nil
	}))
	p := dispatchTestReport(1)
	p.Header.Count = 32 // RTCP count is five bits; the serializer must reject this.
	if _, err := writer.Write([]rtcp.Packet{p}, nil); err == nil {
		t.Fatal("malformed feedback admitted")
	}
	if got := d.snapshot(time.Now()); got.RejectedInvalid != 1 || got.Accepted != 0 || got.QueuedBytes != 0 {
		t.Fatalf("invalid state: %+v", got)
	}
}
