package webrtc

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func timingTestSession() *upstreamTimingSession {
	return &upstreamTimingSession{scope: StreamTimingRequest{StreamRouteRequest: StreamRouteRequest{SSRC: 42}}, started: time.Now(), done: make(chan struct{}), reason: "recording"}
}
func timingTestHeader(t *testing.T, seq, tsn uint16) rtp.Header {
	t.Helper()
	h := rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq}
	if err := h.SetExtension(3, []byte{byte(tsn >> 8), byte(tsn)}); err != nil {
		t.Fatal(err)
	}
	return h
}
func timingTestFeedback() *rtcp.TransportLayerCC {
	return &rtcp.TransportLayerCC{Header: rtcp.Header{Count: rtcp.FormatTCC, Type: rtcp.TypeTransportSpecificFeedback}, SenderSSRC: 10, MediaSSRC: 999, BaseSequenceNumber: 65535, PacketStatusCount: 3, ReferenceTime: 0xffffff, FbPktCount: 255, PacketChunks: []rtcp.PacketStatusChunk{&rtcp.StatusVectorChunk{Type: rtcp.TypeTCCStatusVectorChunk, SymbolSize: 1, SymbolList: []uint16{1, 0, 2}}}, RecvDeltas: []*rtcp.RecvDelta{{Type: 1, Delta: 250}, {Type: 2, Delta: -500}}}
}
func TestUpstreamTimingCopiesQuantizedModularReport(t *testing.T) {
	s := timingTestSession()
	p := timingTestFeedback()
	indexes := s.beginFeedback([]rtcp.Packet{p}, time.Now())
	s.endFeedback(indexes, 28, nil, time.Now())
	s.recordRTP(timingTestHeader(t, 65535, 65535), 3, time.Now())
	s.recordRTP(timingTestHeader(t, 0, 0), 3, time.Now())
	s.stop("complete", time.Now())
	out := s.snapshot(time.Now())
	if len(out.Feedback) != 1 || out.Feedback[0].MediaSSRC != 999 || !reflect.DeepEqual(out.Feedback[0].DeltasUS, []int64{250, -500}) || out.Feedback[0].ReferenceTime != 0xffffff || out.Feedback[0].BaseSequence != 65535 {
		t.Fatalf("report: %+v", out)
	}
	if out.MatchValidity != nil || out.ScopeContinuity != nil || out.RouteContinuity != nil || out.RTP[1].TransportSequence != 0 {
		t.Fatal("modular or unknown scope claimed resolved")
	}
	p.RecvDeltas[0].Delta = 9000
	out.Feedback[0].DeltasUS[0] = 1000
	*out.Feedback[0].WriteOK = false
	out.RTP[0].SSRC = 9
	again := s.snapshot(time.Now())
	if again.Feedback[0].DeltasUS[0] != 250 || !*again.Feedback[0].WriteOK || again.RTP[0].SSRC != 42 {
		t.Fatal("snapshot retained mutable aliases")
	}
}
func TestUpstreamTimingMalformedFeedback(t *testing.T) {
	cases := map[string]func(*rtcp.TransportLayerCC){
		"nil delta":    func(p *rtcp.TransportLayerCC) { p.RecvDeltas[0] = nil },
		"nonquantized": func(p *rtcp.TransportLayerCC) { p.RecvDeltas[0].Delta = 251 },
		"bad symbol":   func(p *rtcp.TransportLayerCC) { p.PacketChunks[0].(*rtcp.StatusVectorChunk).SymbolList[0] = 3 },
		"missing":      func(p *rtcp.TransportLayerCC) { p.RecvDeltas = p.RecvDeltas[:1] },
		"nil chunk":    func(p *rtcp.TransportLayerCC) { p.PacketChunks[0] = (*rtcp.RunLengthChunk)(nil) },
		"zero run":     func(p *rtcp.TransportLayerCC) { p.PacketChunks[0] = &rtcp.RunLengthChunk{RunLength: 0} },
		"huge vector": func(p *rtcp.TransportLayerCC) {
			p.PacketChunks[0].(*rtcp.StatusVectorChunk).SymbolList = make([]uint16, 100)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := timingTestFeedback()
			change(p)
			s := timingTestSession()
			s.beginFeedback([]rtcp.Packet{p}, time.Now())
			if s.reason != "invalid_twcc" || len(s.feedback) != 0 {
				t.Fatalf("accepted malformed report: %s", s.reason)
			}
		})
	}
}
func TestUpstreamTimingDeadlineBoundsAndErrors(t *testing.T) {
	for _, kind := range []string{"deadline", "bytes", "rtp_count", "report_count", "write_error", "missing_extension"} {
		t.Run(kind, func(t *testing.T) {
			s := timingTestSession()
			now := s.started
			switch kind {
			case "deadline":
				now = now.Add(60 * time.Second)
				s.recordRTP(timingTestHeader(t, 1, 1), 3, now)
			case "bytes":
				s.bytes = upstreamTimingMaxBytes
				s.recordRTP(timingTestHeader(t, 1, 1), 3, now)
			case "rtp_count":
				s.rtp = make([]StreamTimingRTP, upstreamTimingMaxRTP)
				s.recordRTP(timingTestHeader(t, 1, 1), 3, now)
			case "report_count":
				s.feedback = make([]StreamTimingFeedback, upstreamTimingMaxReports)
				s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, now)
			case "write_error":
				indexes := s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, now)
				s.endFeedback(indexes, 0, errors.New("private error must not export"), now)
			case "missing_extension":
				s.recordRTP(timingTestHeader(t, 1, 1), 0, now)
			}
			if s.ended.IsZero() {
				t.Fatal("not stopped")
			}
			if s.snapshot(now).Complete {
				t.Fatal("failed or empty data claimed complete")
			}
			count := len(s.rtp)
			s.recordRTP(timingTestHeader(t, 2, 2), 3, now)
			if len(s.rtp) != count {
				t.Fatal("recording continued")
			}
		})
	}
}
func TestUpstreamTimingReaderWriterPreserveTransport(t *testing.T) {
	o := &upstreamFeedbackObserver{}
	s := timingTestSession()
	o.timing.Store(s)
	info := &interceptor.StreamInfo{SSRC: 42, RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: timingTransportURI}}}
	// Bind before arming; binding changes during capture are terminal.
	o.timing.Store(nil)
	h := timingTestHeader(t, 1, 2)
	packet := rtp.Packet{Header: h, Payload: []byte("never retained")}
	raw, err := packet.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	attrs := interceptor.Attributes{"test": "unchanged"}
	reader := o.BindRemoteStream(info, interceptor.RTPReaderFunc(func(b []byte, _ interceptor.Attributes) (int, interceptor.Attributes, error) {
		return copy(b, raw), attrs, nil
	}))
	o.timing.Store(s)
	buffer := make([]byte, len(raw))
	n, returned, err := reader.Read(buffer, nil)
	if err != nil || n != len(raw) || !bytes.Equal(buffer, raw) || !reflect.DeepEqual(attrs, returned) || len(s.rtp) != 1 {
		t.Fatal("reader changed transport")
	}
	packets := []rtcp.Packet{timingTestFeedback()}
	called := 0
	writer := o.BindRTCPWriter(interceptor.RTCPWriterFunc(func(got []rtcp.Packet, a interceptor.Attributes) (int, error) {
		called++
		if got[0] != packets[0] || !reflect.DeepEqual(a, attrs) {
			t.Fatal("writer mutated input")
		}
		return 28, nil
	}))
	if _, err = writer.Write(packets, attrs); err != nil || called != 1 || len(s.feedback) != 1 {
		t.Fatal("writer changed")
	}
	o.UnbindRemoteStream(info)
	if s.reason != "stream_unbound" {
		t.Fatal(s.reason)
	}
}
func timingRouterFixture(t *testing.T) (*Router, StreamTimingRequest, func(*PeerConnectionWrapper) (selectedStreamRoute, error)) {
	t.Helper()
	r, q, route := routeFixture()
	o := &upstreamFeedbackObserver{}
	r.pubPeers[q.PublisherID].upstreamFeedback = o
	read := func(*PeerConnectionWrapper) (selectedStreamRoute, error) { return route, nil }
	result, err := r.streamRoute(q, read)
	if err != nil {
		t.Fatal(err)
	}
	q.RouteToken = result.RouteToken
	return r, StreamTimingRequest{q, result.PeerID, result.IngressStartedAt}, read
}
func TestUpstreamTimingArmReadStopScopeAndRetention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, q, read := timingRouterFixture(t)
		if _, err := r.streamTiming(q, "read", read); err == nil || r.timingDiagnostic != nil {
			t.Fatal("GET armed")
		}
		wrong := q
		wrong.PeerID = "other"
		if _, err := r.streamTiming(wrong, "arm", read); err == nil {
			t.Fatal("wrong peer armed")
		}
		if _, err := r.streamTiming(q, "arm", read); err != nil {
			t.Fatal(err)
		}
		if _, err := r.streamTiming(q, "arm", read); err == nil {
			t.Fatal("duplicate arm")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		r.mu.Lock()
		r.publications[publicationKey{q.PublisherID, q.Slot}]++
		r.mu.Unlock()
		time.Sleep(time.Second)
		synctest.Wait()
		result, err := r.streamTiming(q, "read", read)
		if err != nil || result.Reason != "scope_changed" || result.Complete {
			t.Fatalf("scope: %+v %v", result, err)
		}
		if r.pubPeers[q.PublisherID].upstreamFeedback.timing.Load() != nil {
			t.Fatal("not detached")
		}
		time.Sleep(6 * time.Minute)
		synctest.Wait()
		if r.timingDiagnostic != nil {
			t.Fatal("record retained forever")
		}
	})
}
func TestUpstreamTimingIdleDeadlineNoBackgroundRouteGetter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, q, read := timingRouterFixture(t)
		calls := 0
		wrapped := func(p *PeerConnectionWrapper) (selectedStreamRoute, error) { calls++; return read(p) }
		if _, err := r.streamTiming(q, "arm", wrapped); err != nil {
			t.Fatal(err)
		}
		initial := calls
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if calls != initial || r.timingDiagnostic.snapshot(time.Now()).Reason != "complete" || r.pubPeers[q.PublisherID].upstreamFeedback.timing.Load() != nil {
			t.Fatal("deadline or unexpected background route getter")
		}
	})
}
func TestUpstreamTimingMetadataOnlyUntilSettled(t *testing.T) {
	s := timingTestSession()
	s.recordRTP(timingTestHeader(t, 1, 2), 3, time.Now())
	indexes := s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, time.Now())
	if got := s.snapshot(time.Now()); got.DataAvailable || len(got.RTP) != 0 || got.RTPCount != 1 || got.PendingReports != 1 {
		t.Fatalf("active copy: %+v", got)
	}
	s.stop("complete", s.started.Add(time.Minute))
	if got := s.snapshot(time.Now()); got.Settled || got.Complete || got.DataAvailable {
		t.Fatal("pending write claimed settled")
	}
	s.endFeedback(indexes, 28, nil, s.started.Add(61*time.Second))
	got := s.snapshot(time.Now())
	if !got.Settled || !got.DataAvailable || *got.Feedback[0].EndUS != 61000000 {
		t.Fatalf("late outcome not explicit %+v", got)
	}
}
func TestUpstreamTimingContentionDoesNotBlockPacket(t *testing.T) {
	s := timingTestSession()
	s.mu.Lock()
	s.recordRTP(timingTestHeader(t, 1, 2), 3, time.Now())
	s.mu.Unlock()
	if !s.contended.Load() || s.snapshot(time.Now()).Complete {
		t.Fatal("contention not invalidated")
	}
}
func TestUpstreamTimingCloseStopsInstalledLease(t *testing.T) {
	o := &upstreamFeedbackObserver{}
	s := timingTestSession()
	o.timing.Store(s)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if o.timing.Load() != nil || !o.closed || s.reason != "peer_closed" {
		t.Fatal("close lost lease")
	}
}

func TestUpstreamTimingRetentionReleasesPendingWrite(t *testing.T) {
	s := timingTestSession()
	s.recordRTP(timingTestHeader(t, 1, 2), 3, time.Now())
	indexes := s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, time.Now())
	s.stop("complete", time.Now())
	s.expire()
	s.endFeedback(indexes, 28, nil, time.Now())
	if s.rtp != nil || s.feedback != nil || s.snapshot(time.Now()).DataAvailable {
		t.Fatal("expired data retained or late result repopulated")
	}
}
func TestUpstreamTimingSkippedChecksAndObserverClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, q, read := timingRouterFixture(t)
		if _, err := r.streamTiming(q, "arm", read); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		time.Sleep(time.Second)
		synctest.Wait()
		r.mu.Unlock()
		if r.timingDiagnostic.snapshot(time.Now()).SkippedScopeChecks == 0 {
			t.Fatal("skipped scope not explicit")
		}
		if err := r.pubPeers[q.PublisherID].upstreamFeedback.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if r.timingDiagnostic.snapshot(time.Now()).Reason != "peer_closed" {
			t.Fatal("close not terminal")
		}
	})
}

// A finite synthetic concurrency check, not a live media/performance acceptance test.
func TestUpstreamTimingConcurrentCadence(t *testing.T) {
	s := timingTestSession()
	s.rtp = make([]StreamTimingRTP, 0, upstreamTimingMaxRTP)
	s.feedback = make([]StreamTimingFeedback, 0, upstreamTimingMaxReports)
	stop := make(chan struct{})
	done := make(chan int, 2)
	go func() {
		ticker := time.NewTicker(500 * time.Microsecond)
		defer ticker.Stop()
		count := 0
		h := timingTestHeader(t, 0, 0)
		for {
			select {
			case <-stop:
				done <- count
				return
			case now := <-ticker.C:
				count++
				h.SequenceNumber++
				s.recordRTP(h, 3, now)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		count := 0
		p := timingTestFeedback()
		p.PacketStatusCount = 100
		p.PacketChunks = []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: 1, RunLength: 100}}
		p.RecvDeltas = make([]*rtcp.RecvDelta, 100)
		for i := range p.RecvDeltas {
			p.RecvDeltas[i] = &rtcp.RecvDelta{Type: 1, Delta: 1000}
		}
		for {
			select {
			case <-stop:
				done <- count
				return
			case now := <-ticker.C:
				count++
				indexes := s.beginFeedback([]rtcp.Packet{p}, now)
				s.endFeedback(indexes, 128, nil, time.Now())
			}
		}
	}()
	time.Sleep(3 * time.Second)
	close(stop)
	a, b := <-done, <-done
	s.stop("operator_stopped", time.Now())
	got := s.snapshot(time.Now())
	t.Logf("3s attempted RTP/reports (unordered)=%d/%d recorded=%d/%d contention=%v reason=%s", a, b, got.RTPCount, got.FeedbackCount, got.Contended, got.Reason)
	rtpCalls, reportCalls := max(a, b), min(a, b)
	if got.RTPCount+int(got.DroppedRTP) != rtpCalls || got.FeedbackCount+int(got.DroppedReports) != reportCalls {
		t.Fatal("diagnostic omissions were not counted exactly")
	}
}

func TestUpstreamTimingReadDeadlineIsNotFatal(t *testing.T) {
	o := &upstreamFeedbackObserver{}
	s := timingTestSession()
	info := &interceptor.StreamInfo{SSRC: 42, RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: timingTransportURI}}}
	calls := 0
	packet := rtp.Packet{Header: timingTestHeader(t, 1, 2), Payload: []byte("private")}
	raw, err := packet.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	reader := o.BindRemoteStream(info, interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		calls++
		if calls == 1 {
			return 0, a, os.ErrDeadlineExceeded
		}
		return copy(b, raw), a, nil
	}))
	o.timing.Store(s)
	buffer := make([]byte, len(raw))
	if _, _, err = reader.Read(buffer, nil); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("timeout transport result altered")
	}
	if s.stopped.Load() || s.readTimeouts.Load() != 1 {
		t.Fatal("normal reorder timeout ended recording")
	}
	if _, _, err = reader.Read(buffer, nil); err != nil || len(s.rtp) != 1 {
		t.Fatal("capture did not resume after timeout")
	}
}
func TestUpstreamTimingOutcomeNeverWaitsForDiagnosticMutex(t *testing.T) {
	s := timingTestSession()
	indexes := s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, time.Now())
	s.mu.Lock()
	s.endFeedback(nil, 0, nil, time.Now())
	s.endFeedback(indexes, 28, nil, time.Now())
	s.mu.Unlock()
	if s.pendingReports.Load() != 0 || s.outcomes[indexes[0]].Load() == nil {
		t.Fatal("outcome not published")
	}
}
func TestUpstreamTimingStaleStoppedPointerCannotAddContention(t *testing.T) {
	s := timingTestSession()
	s.stop("complete", time.Now())
	s.mu.Lock()
	s.recordRTP(timingTestHeader(t, 1, 2), 3, time.Now())
	s.beginFeedback([]rtcp.Packet{timingTestFeedback()}, time.Now())
	s.mu.Unlock()
	if s.contended.Load() || s.droppedRTP.Load() != 0 || s.droppedReports.Load() != 0 {
		t.Fatal("stale callback changed completed metadata")
	}
}
