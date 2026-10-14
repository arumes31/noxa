package webrtc

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const upstreamTimingDuration = 60 * time.Second
const upstreamTimingRetention = 5 * time.Minute
const upstreamTimingMaxRTP = 131072
const upstreamTimingMaxReports = 4096
const upstreamTimingMaxBytes = 16 * 1024 * 1024
const timingTransportURI = "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"

// Operator-only, numeric metadata. This is never included in client diagnostics.
type StreamTimingRequest struct {
	StreamRouteRequest
	PeerID           string
	IngressStartedAt int64
}
type StreamTimingRTP struct {
	AtUS              int64  `json:"at_us"`
	SSRC              uint32 `json:"ssrc"`
	Sequence          uint16 `json:"sequence"`
	TransportSequence uint16 `json:"transport_sequence"`
	// Raw modular sequences are deliberately not claimed to be unique match keys.
}
type StreamTimingFeedback struct {
	AtUS          int64    `json:"at_us"`
	EndUS         *int64   `json:"end_us"`
	WriteOK       *bool    `json:"write_ok"`
	WriteBytes    *int     `json:"write_bytes"`
	SenderSSRC    uint32   `json:"sender_ssrc"`
	MediaSSRC     uint32   `json:"media_ssrc"`
	BaseSequence  uint16   `json:"base_sequence"`
	StatusCount   uint16   `json:"status_count"`
	ReferenceTime uint32   `json:"reference_time"`
	FeedbackCount uint8    `json:"feedback_count"`
	Chunks        []uint16 `json:"chunks"`
	DeltaTypes    []uint16 `json:"delta_types"`
	DeltasUS      []int64  `json:"deltas_us"`
}
type StreamTimingDiagnostics struct {
	DroppedRTP           uint64 `json:"dropped_rtp"`
	DroppedReports       uint64 `json:"dropped_reports"`
	ReadTimeouts         uint64 `json:"read_timeouts"`
	Contended            bool   `json:"contended"`
	HookCount            uint64 `json:"rtp_hook_count"`
	HookTotalNS          uint64 `json:"rtp_hook_total_ns"`
	HookMaxNS            uint64 `json:"rtp_hook_max_ns"`
	Settled              bool   `json:"settled"`
	DataAvailable        bool   `json:"data_available"`
	PendingReports       int    `json:"pending_reports"`
	RTPCount             int    `json:"rtp_count"`
	FeedbackCount        int    `json:"feedback_count"`
	SkippedScopeChecks   uint64 `json:"skipped_scope_checks"`
	EndRouteMatches      *bool  `json:"end_route_matches"`
	EndRouteCheckedAt    *int64 `json:"end_route_checked_at"`
	Schema               int    `json:"schema"`
	MaxDurationMS        int64  `json:"max_duration_ms"`
	MaxRTP               int    `json:"max_rtp"`
	MaxReports           int    `json:"max_reports"`
	MaxBudgetBytes       int    `json:"max_budget_bytes"`
	BudgetBytes          int    `json:"budget_bytes"`
	ScopeCheckIntervalMS int    `json:"scope_check_interval_ms"`
	ScopeContinuity      *bool  `json:"scope_continuity"`
	SequenceMatching     string `json:"sequence_matching"`
	StartedAt            int64  `json:"started_at"`
	EndedAt              *int64 `json:"ended_at"`
	ElapsedUS            int64  `json:"elapsed_us"`
	Reason               string `json:"reason"`
	Complete             bool   `json:"complete"`
	Overflow             bool   `json:"overflow"`
	ScopeChecks          uint64 `json:"scope_checks"`
	RouteContinuity      *bool  `json:"route_continuity"`
	MatchValidity        *bool  `json:"match_validity"`
	// UTC endpoints bracket the local monotonic interval; no kernel clock bridge is inferred.
	PeerID           string                 `json:"peer_id"`
	IngressStartedAt int64                  `json:"ingress_started_at"`
	Generation       uint64                 `json:"generation,string"`
	SSRC             uint32                 `json:"ssrc"`
	RouteToken       string                 `json:"route_token"`
	RTP              []StreamTimingRTP      `json:"rtp"`
	Feedback         []StreamTimingFeedback `json:"feedback"`

	ReadStages *StreamTimingReadStages `json:"read_stages,omitempty"`
}
type timingWriteOutcome struct {
	endedUS int64
	ok      bool
	bytes   int
}
type upstreamTimingSession struct {
	readStages     *upstreamReadStageState
	stopped        atomic.Bool
	droppedRTP     atomic.Uint64
	droppedReports atomic.Uint64
	readTimeouts   atomic.Uint64
	writeFailed    atomic.Bool
	pendingReports atomic.Int64
	outcomes       [upstreamTimingMaxReports]atomic.Pointer[timingWriteOutcome]
	expired        bool
	contended      atomic.Bool
	hookCount      atomic.Uint64
	hookTotalNS    atomic.Uint64
	hookMaxNS      atomic.Uint64
	mu             sync.Mutex
	scope          StreamTimingRequest
	started        time.Time
	ended          time.Time
	reason         string
	overflow       bool
	bytes          int
	checks         uint64
	skipped        uint64
	routeEnd       *bool
	routeEndAt     *int64
	rtp            []StreamTimingRTP
	feedback       []StreamTimingFeedback
	done           chan struct{}
	guard          func() (bool, bool) // Only router identity checks; never ICE or network I/O.
}

func timingExtensionID(info *interceptor.StreamInfo) uint8 {
	if info == nil {
		return 0
	}
	var id uint8
	for _, ext := range info.RTPHeaderExtensions {
		if ext.URI == timingTransportURI {
			if id != 0 || ext.ID < 1 || ext.ID > 255 {
				return 0
			}
			id = uint8(ext.ID)
		}
	}
	return id
}
func (s *upstreamTimingSession) stopLocked(reason string, now time.Time) {
	if !s.ended.IsZero() {
		return
	}
	s.stopped.Store(true)
	s.ended, s.reason = now, reason
	close(s.done)
}
func (s *upstreamTimingSession) stop(reason string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked(reason, now)
}
func (s *upstreamTimingSession) scopeOK(now time.Time) bool {
	if s.guard == nil {
		return true
	}
	valid, checked := s.guard()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended.IsZero() {
		return false
	}
	if s.writeFailed.Load() {
		s.stopLocked("write_error", now)
		return false
	}
	if !checked {
		s.skipped++
		return true
	}
	s.checks++
	if !valid {
		s.stopLocked("scope_changed", now)
		return false
	}
	return true
}
func (s *upstreamTimingSession) admitLocked(now time.Time, bytes int) bool {
	if !s.ended.IsZero() {
		return false
	}
	if s.writeFailed.Load() {
		s.stopLocked("write_error", now)
		return false
	}
	if now.Before(s.started) {
		s.stopLocked("clock_error", now)
		return false
	}
	if !now.Before(s.started.Add(upstreamTimingDuration)) {
		s.stopLocked("complete", now)
		return false
	}
	if bytes > upstreamTimingMaxBytes-s.bytes {
		s.overflow = true
		s.stopLocked("overflow", now)
		return false
	}
	s.bytes += bytes
	return true
}
func (s *upstreamTimingSession) recordRTP(header rtp.Header, id uint8, now time.Time) {
	if s.stopped.Load() {
		return
	}
	if header.SSRC != s.scope.SSRC {
		s.stop("ssrc_changed", now)
		return
	}
	ext := header.GetExtension(id)
	if id == 0 || len(ext) != 2 {
		s.stop("transport_extension_unavailable", now)
		return
	}
	if !s.mu.TryLock() {
		if !s.stopped.Load() {
			s.contended.Store(true)
			s.droppedRTP.Add(1)
		}
		return
	}
	defer s.mu.Unlock()
	if !s.ended.IsZero() {
		return
	}
	if len(s.rtp) >= upstreamTimingMaxRTP {
		s.overflow = true
		s.stopLocked("overflow", now)
		return
	}
	// Budget includes spare slice capacity, not just serialized data.
	if !s.admitLocked(now, 64) {
		return
	}
	s.rtp = append(s.rtp, StreamTimingRTP{now.Sub(s.started).Microseconds(), header.SSRC, header.SequenceNumber, binary.BigEndian.Uint16(ext)})
}

// Copy exact report values; no interpretation by MediaSSRC and no packet/payload retained.
func copyTimingFeedback(p *rtcp.TransportLayerCC) (StreamTimingFeedback, int, error) {
	out := StreamTimingFeedback{}
	if p == nil || p.PacketStatusCount == 0 || len(p.PacketChunks) > 32768 || len(p.RecvDeltas) > 32768 {
		return out, 0, errors.New("invalid twcc")
	}
	out.SenderSSRC, out.MediaSSRC, out.BaseSequence, out.StatusCount = p.SenderSSRC, p.MediaSSRC, p.BaseSequenceNumber, p.PacketStatusCount
	out.ReferenceTime, out.FeedbackCount = p.ReferenceTime, p.FbPktCount
	count, di := 0, 0
	for _, chunk := range p.PacketChunks {
		if count >= int(p.PacketStatusCount) {
			return out, 0, errors.New("extra chunk")
		}
		if chunk == nil {
			return out, 0, errors.New("invalid chunk")
		}
		var symbols []uint16
		switch c := chunk.(type) {
		case *rtcp.RunLengthChunk:
			if c == nil || c.Type != rtcp.TypeTCCRunLengthChunk || c.RunLength == 0 || int(c.RunLength) > int(p.PacketStatusCount)-count {
				return out, 0, errors.New("invalid run")
			}
			symbols = make([]uint16, int(c.RunLength))
			for i := range symbols {
				symbols[i] = c.PacketStatusSymbol
			}
		case *rtcp.StatusVectorChunk:
			if c == nil || c.Type != rtcp.TypeTCCStatusVectorChunk || (c.SymbolSize != 0 && c.SymbolSize != 1) || len(c.SymbolList) == 0 || len(c.SymbolList) > 14 || (c.SymbolSize == 1 && len(c.SymbolList) > 7) {
				return out, 0, errors.New("invalid vector")
			}
			if c.SymbolSize == 0 {
				for _, symbol := range c.SymbolList {
					if symbol > 1 {
						return out, 0, errors.New("invalid one-bit symbol")
					}
				}
			}
			symbols = c.SymbolList
		default:
			return out, 0, errors.New("unknown chunk")
		}
		raw, err := chunk.Marshal()
		if err != nil || len(raw) != 2 {
			return out, 0, errors.New("invalid chunk")
		}
		word := binary.BigEndian.Uint16(raw)
		out.Chunks = append(out.Chunks, word)
		for _, symbol := range symbols {
			if count == int(p.PacketStatusCount) {
				break
			}
			if symbol > 2 {
				return out, 0, errors.New("unsupported status")
			}
			count++
			if symbol == 0 {
				continue
			}
			if di >= len(p.RecvDeltas) || p.RecvDeltas[di] == nil {
				return out, 0, errors.New("missing delta")
			}
			d := p.RecvDeltas[di]
			di++
			if d.Type != symbol || d.Delta%250 != 0 || (symbol == 1 && (d.Delta < 0 || d.Delta > 63750)) || d.Delta < -8192000 || d.Delta > 8191750 {
				return out, 0, errors.New("invalid delta")
			}
			out.DeltaTypes = append(out.DeltaTypes, d.Type)
			out.DeltasUS = append(out.DeltasUS, d.Delta)
		}
	}
	if count != int(p.PacketStatusCount) || di != len(p.RecvDeltas) {
		return out, 0, errors.New("incomplete twcc")
	}
	return out, 256 + 4*len(out.Chunks) + 32*len(out.DeltasUS), nil
}
func (s *upstreamTimingSession) beginFeedback(packets []rtcp.Packet, now time.Time) []int {
	if s.stopped.Load() {
		return nil
	}
	if !s.mu.TryLock() {
		if !s.stopped.Load() {
			s.contended.Store(true)
			for _, p := range packets {
				if _, ok := p.(*rtcp.TransportLayerCC); ok {
					s.droppedReports.Add(1)
				}
			}
		}
		return nil
	}
	defer s.mu.Unlock()
	var indexes []int
	for _, packet := range packets {
		p, ok := packet.(*rtcp.TransportLayerCC)
		if !ok {
			continue
		}
		if !s.ended.IsZero() {
			break
		}
		if len(s.feedback) >= upstreamTimingMaxReports {
			s.overflow = true
			s.stopLocked("overflow", now)
			break
		}
		row, size, err := copyTimingFeedback(p)
		if err != nil {
			s.stopLocked("invalid_twcc", now)
			break
		}
		if !s.admitLocked(now, size) {
			break
		}
		row.AtUS = now.Sub(s.started).Microseconds()
		s.pendingReports.Add(1)
		indexes = append(indexes, len(s.feedback))
		s.feedback = append(s.feedback, row)
	}
	return indexes
}
func (s *upstreamTimingSession) endFeedback(indexes []int, n int, err error, now time.Time) {
	if len(indexes) == 0 {
		return
	}
	outcome := &timingWriteOutcome{now.Sub(s.started).Microseconds(), err == nil && n > 0, n}
	for _, i := range indexes {
		if s.outcomes[i].CompareAndSwap(nil, outcome) {
			s.pendingReports.Add(-1)
		}
	}
	if !outcome.ok {
		s.writeFailed.Store(true)
		if s.mu.TryLock() {
			s.stopLocked("write_error", now)
			s.mu.Unlock()
		}
	}
}
func (s *upstreamTimingSession) snapshot(now time.Time) *StreamTimingDiagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &StreamTimingDiagnostics{DroppedRTP: s.droppedRTP.Load(), DroppedReports: s.droppedReports.Load(), ReadTimeouts: s.readTimeouts.Load(), Contended: s.contended.Load(), HookCount: s.hookCount.Load(), HookTotalNS: s.hookTotalNS.Load(), HookMaxNS: s.hookMaxNS.Load(), PendingReports: int(s.pendingReports.Load()), RTPCount: len(s.rtp), FeedbackCount: len(s.feedback), SkippedScopeChecks: s.skipped, Schema: 1, MaxDurationMS: 60000, MaxRTP: upstreamTimingMaxRTP, MaxReports: upstreamTimingMaxReports, MaxBudgetBytes: upstreamTimingMaxBytes, BudgetBytes: s.bytes, ScopeCheckIntervalMS: 500, SequenceMatching: "raw_modular_unresolved", StartedAt: s.started.UnixMilli(), ElapsedUS: now.Sub(s.started).Microseconds(), Reason: s.reason, Overflow: s.overflow, ScopeChecks: s.checks, PeerID: s.scope.PeerID, IngressStartedAt: s.scope.IngressStartedAt, Generation: s.scope.Generation, SSRC: s.scope.SSRC, RouteToken: s.scope.RouteToken}
	if !s.ended.IsZero() {
		at := s.ended.UnixMilli()
		out.EndedAt = &at
		out.ElapsedUS = s.ended.Sub(s.started).Microseconds()
	}
	out.Settled = !s.ended.IsZero() && out.PendingReports == 0
	out.ReadStages = s.readStageSnapshotLocked(out.Settled)
	if s.expired {
		out.Reason = "expired"
		return out
	}
	if s.routeEnd != nil {
		v := *s.routeEnd
		out.EndRouteMatches = &v
	}
	if s.routeEndAt != nil {
		v := *s.routeEndAt
		out.EndRouteCheckedAt = &v
	}
	out.Complete = s.reason == "complete" && !s.overflow && !s.contended.Load() && !s.writeFailed.Load()
	if out.ReadStages != nil && (out.ReadStages.DroppedUpdates != 0 || out.ReadStages.OmittedRows != 0) {
		out.Complete = false
	}
	if !out.Settled {
		out.Complete = false
		return out
	}
	out.DataAvailable = true
	out.RTP = append([]StreamTimingRTP{}, s.rtp...)
	out.Feedback = make([]StreamTimingFeedback, len(s.feedback))
	for i, row := range s.feedback {
		if outcome := s.outcomes[i].Load(); outcome != nil {
			ended, ok, n := outcome.endedUS, outcome.ok, outcome.bytes
			row.EndUS, row.WriteOK, row.WriteBytes = &ended, &ok, &n
		}
		out.Feedback[i] = row
		if row.EndUS != nil {
			v := *row.EndUS
			out.Feedback[i].EndUS = &v
		}
		if row.WriteOK != nil {
			v := *row.WriteOK
			out.Feedback[i].WriteOK = &v
		}
		if row.WriteBytes != nil {
			v := *row.WriteBytes
			out.Feedback[i].WriteBytes = &v
		}
		out.Feedback[i].Chunks = append([]uint16{}, row.Chunks...)
		out.Feedback[i].DeltaTypes = append([]uint16{}, row.DeltaTypes...)
		out.Feedback[i].DeltasUS = append([]int64{}, row.DeltasUS...)
		if row.WriteOK == nil || !*row.WriteOK {
			out.Complete = false
		}
	}
	if len(out.RTP) == 0 || len(out.Feedback) == 0 {
		out.Complete = false
	}
	return out
}

func (s *upstreamTimingSession) observeRTPHook(started time.Time) {
	if s.stopped.Load() {
		return
	}
	ns := uint64(max(int64(0), time.Since(started).Nanoseconds()))
	s.hookCount.Add(1)
	s.hookTotalNS.Add(ns)
	for old := s.hookMaxNS.Load(); ns > old; old = s.hookMaxNS.Load() {
		if s.hookMaxNS.CompareAndSwap(old, ns) {
			break
		}
	}
}

// Release bulk data even if a foreign transport write never returns. Its eventual
// callback must not access the old report indexes after retention expires.
func (s *upstreamTimingSession) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired = true
	s.rtp = nil
	s.feedback = nil
	if s.readStages != nil {
		s.readStages.data.Rows = nil
		s.readStages.hasOpen = false
	}
}
