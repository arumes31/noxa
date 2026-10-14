package webrtc

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

const upstreamReadStageThreshold = 20 * time.Millisecond
const upstreamReadStageRows = 2048

// Includes reserved row capacity and fixed state, not just populated rows.
const upstreamReadStageBudget = upstreamReadStageRows*128 + 1024

type StreamTimingReadStage struct {
	Ordinal  uint64 `json:"ordinal"`
	Kind     string `json:"kind"`
	ReadKind string `json:"read_kind"`
	StartUS  int64  `json:"start_us"`
	EndUS    int64  `json:"end_us"`
	Result   string `json:"result"`
	Clipped  bool   `json:"clipped"`
}
type StreamTimingStageTotals struct {
	Count   uint64 `json:"count"`
	TotalUS uint64 `json:"total_us"`
	MaxUS   uint64 `json:"max_us"`
}
type StreamTimingReadStages struct {
	BoundaryOmitted      bool                    `json:"boundary_omitted"`
	Schema               int                     `json:"schema"`
	ThresholdUS          int64                   `json:"threshold_us"`
	MaxRows              int                     `json:"max_rows"`
	ReadsStarted         uint64                  `json:"reads_started"`
	FirstBoundaryOmitted bool                    `json:"first_boundary_omitted"`
	DroppedUpdates       uint64                  `json:"dropped_updates"`
	OmittedRows          uint64                  `json:"omitted_rows"`
	LoopExit             string                  `json:"loop_exit"`
	Read                 StreamTimingStageTotals `json:"read_rtp"`
	Between              StreamTimingStageTotals `json:"between_reads"`
	Rows                 []StreamTimingReadStage `json:"rows"`
	Boundary             *StreamTimingReadStage  `json:"boundary"`
}
type upstreamReadStageState struct {
	data        StreamTimingReadStages // Protected by the existing session mutex.
	dropped     atomic.Uint64
	open        StreamTimingReadStage
	hasOpen     bool
	openDropped uint64
}

func newUpstreamReadStageState() *upstreamReadStageState {
	return &upstreamReadStageState{data: StreamTimingReadStages{Schema: 1, ThresholdUS: upstreamReadStageThreshold.Microseconds(), MaxRows: upstreamReadStageRows, FirstBoundaryOmitted: true, Rows: make([]StreamTimingReadStage, 0, upstreamReadStageRows)}}
}

// One tracker belongs to one readOrderedVideo invocation. A new ingress has a
// different atomic pointer even if its SSRC is reused. No global/router lookup.
type upstreamReadStageTracker struct {
	input    *videoIngressDiagnostic
	session  *upstreamTimingSession
	ordinal  uint64
	previous time.Time
}

func (t *upstreamReadStageTracker) read(track VideoTrackReader, repair bool) (*rtp.Packet, interceptor.Attributes, error) {
	if t == nil || t.input == nil {
		return track.ReadRTP()
	}
	s := t.input.readTiming.Load()
	if s == nil || s.stopped.Load() {
		t.session = nil
		t.previous = time.Time{}
		return track.ReadRTP()
	}
	entered := time.Now()
	if entered.Before(s.started) || !entered.Before(s.started.Add(upstreamTimingDuration)) {
		t.session = nil
		t.previous = time.Time{}
		return track.ReadRTP()
	}
	if t.session != s {
		t.session = s
		t.ordinal = 0
		t.previous = time.Time{}
	}
	t.ordinal++
	ordinal := t.ordinal
	kind := "ordinary"
	if repair {
		kind = "repair_drain"
	}
	s.beginReadStage(ordinal, kind, t.previous, entered)
	packet, attrs, err := track.ReadRTP()
	returned := time.Now()
	// Never label a return with a newly armed session or after cancellation.
	if t.input.readTiming.Load() == s && !s.stopped.Load() && returned.Before(s.started.Add(upstreamTimingDuration)) {
		result := "empty"
		switch {
		case err != nil:
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				result = "timeout"
			} else {
				result = "error"
			}
		case packet != nil:
			result = "packet"
		}
		s.endReadStage(ordinal, kind, entered, returned, result)
		t.previous = returned
	} else {
		t.previous = time.Time{}
	}
	return packet, attrs, err
}
func (t *upstreamReadStageTracker) finish(reason string) {
	if t == nil || t.session == nil || t.previous.IsZero() {
		return
	}
	s := t.session
	if t.input.readTiming.Load() != s || s.stopped.Load() {
		return
	}
	now := time.Now()
	if !now.Before(s.started.Add(upstreamTimingDuration)) {
		return
	}
	if !s.mu.TryLock() {
		if !s.stopped.Load() {
			s.readStages.dropped.Add(1)
		}
		return
	}
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return
	}
	s.addReadStageLocked(StreamTimingReadStage{Ordinal: t.ordinal, Kind: "between_reads", ReadKind: "loop_exit", StartUS: t.previous.Sub(s.started).Microseconds(), EndUS: now.Sub(s.started).Microseconds(), Result: reason})
	s.readStages.data.LoopExit = reason
	s.readStages.hasOpen = false
}
func (s *upstreamTimingSession) beginReadStage(ordinal uint64, kind string, previous, now time.Time) {
	if !s.mu.TryLock() {
		if !s.stopped.Load() {
			s.readStages.dropped.Add(1)
		}
		return
	}
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return
	}
	d := s.readStages
	if !previous.IsZero() {
		s.addReadStageLocked(StreamTimingReadStage{Ordinal: ordinal - 1, Kind: "between_reads", ReadKind: kind, StartUS: previous.Sub(s.started).Microseconds(), EndUS: now.Sub(s.started).Microseconds(), Result: "next_read"})
	}
	d.data.ReadsStarted++
	d.open = StreamTimingReadStage{Ordinal: ordinal, Kind: "read_rtp", ReadKind: kind, StartUS: now.Sub(s.started).Microseconds()}
	d.hasOpen = true
	d.openDropped = d.dropped.Load()
}
func (s *upstreamTimingSession) endReadStage(ordinal uint64, kind string, entered, returned time.Time, result string) {
	if !s.mu.TryLock() {
		if !s.stopped.Load() {
			s.readStages.dropped.Add(1)
		}
		return
	}
	defer s.mu.Unlock()
	if s.stopped.Load() {
		return
	}
	s.addReadStageLocked(StreamTimingReadStage{Ordinal: ordinal, Kind: "read_rtp", ReadKind: kind, StartUS: entered.Sub(s.started).Microseconds(), EndUS: returned.Sub(s.started).Microseconds(), Result: result})
	s.readStages.open = StreamTimingReadStage{Ordinal: ordinal, Kind: "between_reads", ReadKind: kind, StartUS: returned.Sub(s.started).Microseconds()}
	s.readStages.hasOpen = true
	s.readStages.openDropped = s.readStages.dropped.Load()
}
func (s *upstreamTimingSession) addReadStageLocked(row StreamTimingReadStage) {
	d := s.readStages
	if row.StartUS < 0 || row.EndUS < row.StartUS || row.EndUS > upstreamTimingDuration.Microseconds() {
		d.dropped.Add(1)
		return
	}
	total := &d.data.Read
	if row.Kind == "between_reads" {
		total = &d.data.Between
	}
	elapsed := uint64(row.EndUS - row.StartUS)
	total.Count++
	total.TotalUS += elapsed
	total.MaxUS = max(total.MaxUS, elapsed)
	if elapsed < uint64(upstreamReadStageThreshold.Microseconds()) && row.Result != "error" && row.Result != "timeout" {
		return
	}
	if len(d.data.Rows) == upstreamReadStageRows {
		d.data.OmittedRows++
		return
	}
	d.data.Rows = append(d.data.Rows, row)
}

// Called under s.mu. A cutoff interval is separate from complete interval totals;
// it never turns an unfinished ReadRTP into apparent processing between reads.
func (s *upstreamTimingSession) readStageSnapshotLocked(includeRows bool) *StreamTimingReadStages {
	if s.readStages == nil {
		return nil
	}
	d := s.readStages
	out := d.data
	out.DroppedUpdates = d.dropped.Load()
	out.Rows = nil
	if includeRows {
		out.Rows = append([]StreamTimingReadStage{}, d.data.Rows...)
		out.BoundaryOmitted = d.hasOpen && d.openDropped != out.DroppedUpdates
		if d.hasOpen && !out.BoundaryOmitted && !s.ended.IsZero() {
			row := d.open
			row.EndUS = min(s.ended.Sub(s.started).Microseconds(), upstreamTimingDuration.Microseconds())
			row.Result = "capture_boundary"
			row.Clipped = true
			if row.EndUS >= row.StartUS {
				out.Boundary = &row
			}
		}
	}
	return &out
}
