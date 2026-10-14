package webrtc

import (
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type stageTrack struct {
	read     func() (*rtp.Packet, interceptor.Attributes, error)
	deadline func(time.Time) error
}

func (*stageTrack) RID() string                                             { return "" }
func (*stageTrack) SSRC() pion.SSRC                                         { return 42 }
func (t *stageTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) { return t.read() }
func (t *stageTrack) SetReadDeadline(at time.Time) error {
	if t.deadline != nil {
		return t.deadline(at)
	}
	return nil
}
func stageFixture() (*upstreamTimingSession, *videoIngressDiagnostic, *upstreamReadStageTracker) {
	s := timingTestSession()
	s.readStages = newUpstreamReadStageState()
	s.bytes = upstreamReadStageBudget
	input := &videoIngressDiagnostic{}
	input.readTiming.Store(s)
	return s, input, &upstreamReadStageTracker{input: input}
}
func stagePacket(seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq}, Payload: []byte{1}}
}
func TestUpstreamReadStagesSeparateReadAndSynchronousEmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, tracker := stageFixture()
		calls := 0
		var emitted []uint16
		track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			calls++
			time.Sleep(30 * time.Millisecond)
			if calls == 3 {
				return nil, nil, io.EOF
			}
			return stagePacket(uint16(calls)), nil, nil
		}}
		readOrderedVideoTimed(track, func() bool { return true }, func() string { return "video/VP8" }, func(p *rtp.Packet, _ string) {
			emitted = append(emitted, p.SequenceNumber)
			time.Sleep(200 * time.Millisecond)
		}, nil, tracker)
		d := s.snapshot(time.Now()).ReadStages
		if !reflect.DeepEqual(emitted, []uint16{1, 2}) || d.Read.Count != 3 || d.Read.MaxUS != 30000 || d.Read.TotalUS != 90000 || d.Between.MaxUS != 200000 || d.LoopExit != "read_error" || !d.FirstBoundaryOmitted {
			t.Fatalf("wrong stages: %+v emitted=%v", d, emitted)
		}
		s.stop("operator_stopped", time.Now())
		d = s.snapshot(time.Now()).ReadStages
		if len(d.Rows) != 5 || d.Boundary != nil || d.Rows[1].Kind != "between_reads" || d.Rows[1].Ordinal != 1 || d.Rows[2].Ordinal != 2 {
			t.Fatalf("wrong intervals: %+v", d)
		}
	})
}
func TestUpstreamReadStagesCoverRepairDrainAndPreserveResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, tracker := stageFixture()
		calls := 0
		var emitted []uint16
		track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			calls++
			time.Sleep(25 * time.Millisecond)
			switch calls {
			case 1:
				return stagePacket(1), nil, nil
			case 2:
				return stagePacket(3), nil, nil
			case 3, 5:
				return nil, nil, os.ErrDeadlineExceeded
			case 4:
				return stagePacket(2), nil, nil
			default:
				return nil, nil, io.EOF
			}
		}}
		readOrderedVideoTimed(track, func() bool { return true }, func() string { return "video/VP8" }, func(p *rtp.Packet, _ string) { emitted = append(emitted, p.SequenceNumber) }, nil, tracker)
		s.stop("operator_stopped", time.Now())
		d := s.snapshot(time.Now()).ReadStages
		if calls != 6 || d.Read.Count != 6 || !reflect.DeepEqual(emitted, []uint16{1, 2, 3}) {
			t.Fatalf("repair changed: calls=%d d=%+v emitted=%v", calls, d, emitted)
		}
		if d.Rows[3].ReadKind != "repair_drain" || d.Rows[4].ReadKind != "repair_drain" || d.Rows[2].Result != "timeout" || d.Rows[4].Result != "timeout" {
			t.Fatalf("missing repair kinds: %+v", d.Rows)
		}
		packet := stagePacket(8)
		attrs := interceptor.Attributes{"same": true}
		sentinel := errors.New("sentinel")
		same := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) { return packet, attrs, sentinel }}
		got, a, err := tracker.read(same, false)
		if got != packet || err != sentinel || !reflect.DeepEqual(a, attrs) {
			t.Fatal("result changed")
		}
	})
}
func TestUpstreamReadStagesArmDuringReadStopAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, input, tracker := stageFixture()
		input.readTiming.Store(nil)
		first := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			time.Sleep(100 * time.Millisecond)
			input.readTiming.Store(s)
			return stagePacket(1), nil, nil
		}}
		_, _, _ = tracker.read(first, false)
		if s.readStages.data.ReadsStarted != 0 {
			t.Fatal("retroactive entry")
		}
		blocked := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			time.Sleep(10 * time.Millisecond)
			s.stop("operator_stopped", time.Now())
			time.Sleep(200 * time.Millisecond)
			return stagePacket(2), nil, nil
		}}
		_, _, _ = tracker.read(blocked, false)
		d := s.snapshot(time.Now()).ReadStages
		if !d.FirstBoundaryOmitted || d.Read.Count != 0 || d.Boundary == nil || d.Boundary.Kind != "read_rtp" || !d.Boundary.Clipped || d.Boundary.EndUS-d.Boundary.StartUS != 10000 {
			t.Fatalf("boundary mislabeled: %+v", d)
		}
		replacement := timingTestSession()
		replacement.readStages = newUpstreamReadStageState()
		input.readTiming.Store(replacement)
		quick := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			time.Sleep(7 * time.Millisecond)
			return nil, nil, nil
		}}
		_, _, _ = tracker.read(quick, false)
		if replacement.readStages.data.ReadsStarted != 1 || replacement.readStages.data.Between.Count != 0 || tracker.ordinal != 1 {
			t.Fatal("cross-session interval")
		}
		foreign := &upstreamReadStageTracker{input: &videoIngressDiagnostic{}}
		_, _, _ = foreign.read(quick, false)
		if replacement.readStages.data.ReadsStarted != 1 {
			t.Fatal("foreign ingress recorded")
		}
	})
}
func TestUpstreamReadStagesExpiryContentionAndBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, tracker := stageFixture()
		track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			time.Sleep(7 * time.Millisecond)
			return stagePacket(1), nil, nil
		}}
		s.mu.Lock()
		_, _, _ = tracker.read(track, false)
		s.mu.Unlock()
		if s.readStages.dropped.Load() != 2 {
			t.Fatal("contention not skipped")
		}
		for i := 0; i < upstreamReadStageRows+1; i++ {
			s.addReadStageLocked(StreamTimingReadStage{Ordinal: uint64(i + 1), Kind: "read_rtp", ReadKind: "ordinary", StartUS: 0, EndUS: 20000, Result: "packet"})
		}
		if len(s.readStages.data.Rows) != upstreamReadStageRows || s.readStages.data.OmittedRows != 1 || s.readStages.data.Read.Count != upstreamReadStageRows+1 || s.bytes != upstreamReadStageBudget {
			t.Fatal("cap or totals")
		}
		s.stop("complete", s.started.Add(upstreamTimingDuration))
		d := s.snapshot(time.Now()).ReadStages
		if d.DroppedUpdates != 2 || s.snapshot(time.Now()).Complete {
			t.Fatal("partial claimed complete")
		}
		s.expire()
		if len(s.readStages.data.Rows) != 0 {
			t.Fatal("retained rows")
		}
		end, _, tr := stageFixture()
		long := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			time.Sleep(61 * time.Second)
			return stagePacket(1), nil, nil
		}}
		_, _, _ = tr.read(long, false)
		end.stop("complete", time.Now())
		boundary := end.snapshot(time.Now()).ReadStages.Boundary
		if end.readStages.data.Read.Count != 0 || boundary == nil || boundary.Kind != "read_rtp" || boundary.EndUS != 60000000 {
			t.Fatal("expiry included out-of-window completion")
		}
	})
}
func TestUpstreamReadStagesExitReasonsAndInactiveAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, reason := range []string{"deadline_error", "scope_changed"} {
			s, _, tracker := stageFixture()
			calls := 0
			track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) { calls++; return stagePacket(1), nil, nil }, deadline: func(time.Time) error {
				if calls > 0 && reason == "deadline_error" {
					return io.ErrClosedPipe
				}
				return nil
			}}
			readOrderedVideoTimed(track, func() bool { return reason != "scope_changed" || calls == 0 }, func() string { return "video/VP8" }, func(*rtp.Packet, string) {}, nil, tracker)
			if s.readStages.data.LoopExit != reason {
				t.Fatalf("exit %s != %s", s.readStages.data.LoopExit, reason)
			}
		}
	})
	tracker := &upstreamReadStageTracker{input: &videoIngressDiagnostic{}}
	packet := stagePacket(1)
	track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) { return packet, nil, nil }}
	if n := testing.AllocsPerRun(100, func() { _, _, _ = tracker.read(track, false) }); n != 0 {
		t.Fatalf("inactive allocations=%v", n)
	}
}
func TestUpstreamReadStagesDoNotInventBoundaryAfterDroppedTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, _ := stageFixture()
		at := time.Now()
		s.beginReadStage(1, "ordinary", time.Time{}, at)
		s.mu.Lock()
		s.endReadStage(1, "ordinary", at, at.Add(time.Millisecond), "packet")
		s.mu.Unlock()
		s.stop("operator_stopped", at.Add(time.Second))
		d := s.snapshot(time.Now()).ReadStages
		if d.Boundary != nil || !d.BoundaryOmitted || d.DroppedUpdates != 1 {
			t.Fatalf("fabricated read boundary: %+v", d)
		}
		recovered, _, _ := stageFixture()
		recovered.beginReadStage(1, "ordinary", time.Time{}, at)
		recovered.mu.Lock()
		recovered.endReadStage(1, "ordinary", at, at.Add(time.Millisecond), "packet")
		recovered.mu.Unlock()
		recovered.beginReadStage(2, "ordinary", at.Add(time.Millisecond), at.Add(2*time.Millisecond))
		recovered.stop("operator_stopped", at.Add(time.Second))
		d = recovered.snapshot(time.Now()).ReadStages
		if d.Boundary == nil || d.Boundary.Ordinal != 2 || d.BoundaryOmitted {
			t.Fatal("known transition did not recover boundary")
		}
	})
}
func TestUpstreamReadStagesArmCleanupAndRepairFatalExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, q, read := timingRouterFixture(t)
		if _, err := r.streamTiming(q, "arm", read); err != nil {
			t.Fatal(err)
		}
		var input *videoIngressDiagnostic
		for _, v := range r.videoIngress {
			if v.ssrc == q.SSRC {
				input = v
				break
			}
		}
		if input == nil || input.readTiming.Load() != r.timingDiagnostic || r.timingDiagnostic.bytes != upstreamReadStageBudget {
			t.Fatal("not attached to exact ingress")
		}
		if _, err := r.streamTiming(q, "stop", read); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if input.readTiming.Load() != nil {
			t.Fatal("stopped session retained")
		}
		s, _, tracker := stageFixture()
		calls := 0
		track := &stageTrack{read: func() (*rtp.Packet, interceptor.Attributes, error) {
			calls++
			switch calls {
			case 1:
				return stagePacket(1), nil, nil
			case 2:
				return stagePacket(3), nil, nil
			case 3:
				return nil, nil, os.ErrDeadlineExceeded
			default:
				return nil, nil, io.EOF
			}
		}}
		readOrderedVideoTimed(track, func() bool { return true }, func() string { return "video/VP8" }, func(*rtp.Packet, string) {}, nil, tracker)
		if s.readStages.data.LoopExit != "read_error" || calls != 4 {
			t.Fatalf("repair fatal=%s calls=%d", s.readStages.data.LoopExit, calls)
		}
	})
}

func TestUpstreamReadStagesStoppedCallbacksAndAutomaticCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, tracker := stageFixture()
		tracker.session = s
		tracker.previous = s.started
		s.stop("operator_stopped", time.Now())
		s.mu.Lock()
		s.beginReadStage(1, "ordinary", time.Time{}, s.started)
		s.endReadStage(1, "ordinary", s.started, s.started.Add(time.Millisecond), "packet")
		tracker.finish("scope_changed")
		s.mu.Unlock()
		if s.readStages.dropped.Load() != 0 {
			t.Fatal("stale callback changed settled record")
		}
		for _, action := range []string{"timer", "peer_close"} {
			r, q, read := timingRouterFixture(t)
			if _, err := r.streamTiming(q, "arm", read); err != nil {
				t.Fatal(err)
			}
			var input *videoIngressDiagnostic
			for _, v := range r.videoIngress {
				if v.ssrc == q.SSRC {
					input = v
					break
				}
			}
			if input == nil || input.readTiming.Load() != r.timingDiagnostic {
				t.Fatal("wrong ingress association")
			}
			if action == "timer" {
				time.Sleep(upstreamTimingDuration + time.Second)
			} else {
				if err := r.pubPeers[q.PublisherID].upstreamFeedback.Close(); err != nil {
					t.Fatal(err)
				}
			}
			synctest.Wait()
			if input.readTiming.Load() != nil || !r.timingDiagnostic.stopped.Load() {
				t.Fatalf("%s pointer not detached", action)
			}
		}
	})
}
