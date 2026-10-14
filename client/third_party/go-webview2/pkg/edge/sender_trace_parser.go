package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

const senderTraceMessageLimit = 8 << 20
const senderTraceRawLimit = 32 << 20
const senderTraceEventLimit = 32768
const senderTraceRetainedLimit = (2 << 20) - 8192

// SenderTraceEvent contains only a fixed marker kind and numeric trace fields.
// TraceUS uses the runtime trace clock, not UTC. These are marker entries, not
// completed frame/pacer spans. RTP values are not sufficient to prove a join.
type SenderTraceEvent struct {
	Kind     string  `json:"kind"`
	TraceUS  float64 `json:"trace_us"`
	RTP      *uint32 `json:"rtp_timestamp,omitempty"`
	Sequence *uint16 `json:"sequence_number,omitempty"`
}

type senderTraceParser struct {
	events        []SenderTraceEvent
	counts        map[string]int
	unknown       int
	rawBytes      int
	retainedBytes int
	// IDs stay private. Multiple producer threads for one marker are ambiguous.
	producer    map[string][2]uint64
	rendererPID *uint64
	failure     string
	diagnostics *senderTraceDiagnostics
}

func strictTraceJSON(raw string, dst any) error {
	if len(raw) > senderTraceMessageLimit {
		return errors.New("message_limit")
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	if err := d.Decode(dst); err != nil {
		return errors.New("malformed_data")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("malformed_data")
	}
	return nil
}
func traceNumber(raw json.RawMessage) (float64, bool) {
	var n float64
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 9007199254740991 {
		return 0, false
	}
	return n, true
}
func traceUint(raw json.RawMessage, max uint64) (uint64, bool) {
	n, ok := traceNumber(raw)
	return uint64(n), ok && n == math.Trunc(n) && n <= float64(max)
}

type senderTraceInput struct {
	Name  string                     `json:"name"`
	Cat   string                     `json:"cat"`
	Ph    string                     `json:"ph"`
	Scope json.RawMessage            `json:"s"`
	TS    json.RawMessage            `json:"ts"`
	PID   json.RawMessage            `json:"pid"`
	TID   json.RawMessage            `json:"tid"`
	Args  map[string]json.RawMessage `json:"args"`
}

func senderTraceMarker(name string) (kind, field string) {
	switch name {
	case "VideoEncoder::Encode":
		return "encode", "rtp_timestamp"
	case "VCMEncodedFrameCallback::Encoded":
		return "encoded", "timestamp"
	case "TaskQueuePacedSender::EnqueuePackets::Loop":
		return "enqueue", "rtp_timestamp"
	case "TaskQueuePacedSender::MaybeProcessPackets":
		return "process", ""
	default:
		return "", ""
	}
}
func senderTraceCategory(kind string) string {
	if kind == "enqueue" || kind == "process" {
		return "disabled-by-default-webrtc"
	}
	return "webrtc"
}

func (p *senderTraceParser) reject(why, kind, check string, e senderTraceInput) string {
	if p.failure == "" {
		p.failure = why
		p.diagnostics.FirstRejection = p.rejection(kind, check, e)
	}
	return p.failure
}

func (p *senderTraceParser) add(raw string) string {
	p.ensureDiagnostics()
	rejectChunk := func(why string) string {
		p.diagnostics.ClassificationTruncated = true
		return p.reject(why, "none", why, senderTraceInput{})
	}
	if len(raw) > senderTraceMessageLimit {
		return rejectChunk("message_limit")
	}
	if p.rawBytes+len(raw) > senderTraceRawLimit {
		return rejectChunk("raw_limit")
	}
	p.rawBytes += len(raw)
	var chunk struct {
		Value []json.RawMessage `json:"value"`
	}
	if strictTraceJSON(raw, &chunk) != nil || chunk.Value == nil {
		return rejectChunk("malformed_data")
	}
	if len(chunk.Value) > senderTraceEventLimit {
		return rejectChunk("event_limit")
	}
	if p.counts == nil {
		p.counts = map[string]int{}
		p.producer = map[string][2]uint64{}
	}
	for _, entry := range chunk.Value {
		if p.diagnostics.EntriesExamined >= senderTraceDiagnosticLimit {
			return rejectChunk("diagnostic_limit")
		}
		p.diagnostics.EntriesExamined++
		if p.failure != "" {
			p.diagnostics.EntriesAfterFreeze++
		}
		var e senderTraceInput
		if json.Unmarshal(entry, &e) != nil {
			p.diagnostics.MalformedEntries++
			p.reject("malformed_data", "none", "malformed_data", senderTraceInput{})
			continue
		}
		kind, field := senderTraceMarker(e.Name)
		if kind != "" {
			p.classify(kind, e)
		}
		// Timing data and producer references are immutable after the first
		// rejection. Delivered tails/flush chunks only add bounded counters.
		if p.failure != "" {
			continue
		}
		if kind == "" {
			p.unknown++
			continue
		}
		if why, check := p.accept(kind, field, e); why != "" {
			p.reject(why, kind, check, e)
		}
	}
	return p.failure
}

func (p *senderTraceParser) accept(kind, field string, e senderTraceInput) (string, string) {
	// Ignore end records and unsupported phases rather than invent paired spans.
	if (kind == "encoded" && e.Ph != "I" && e.Ph != "i") || (kind != "encoded" && e.Ph != "B" && e.Ph != "X") || e.Cat != senderTraceCategory(kind) {
		p.unknown++
		return "", ""
	}
	ts, ok := traceNumber(e.TS)
	if !ok {
		return "malformed_marker", "timestamp"
	}
	pid, pok := traceUint(e.PID, 9007199254740991)
	tid, tok := traceUint(e.TID, 9007199254740991)
	if !pok || !tok {
		return "malformed_marker", "producer_id"
	}
	pair := [2]uint64{pid, tid}
	if old, found := p.producer[kind]; found {
		if old[0] != pid {
			return "ambiguous_marker_scope", "within_kind_pid"
		}
		if old[1] != tid {
			return "ambiguous_marker_scope", "within_kind_tid"
		}
	}
	if p.rendererPID != nil && *p.rendererPID != pid {
		return "ambiguous_marker_scope", "cross_kind_pid"
	}
	out := SenderTraceEvent{Kind: kind, TraceUS: ts}
	if field != "" {
		n, ok := traceUint(e.Args[field], math.MaxUint32)
		if !ok {
			return "malformed_marker", "rtp_timestamp"
		}
		v := uint32(n)
		out.RTP = &v
	}
	if kind == "enqueue" {
		n, ok := traceUint(e.Args["sequence_number"], math.MaxUint16)
		if !ok {
			return "malformed_marker", "sequence_number"
		}
		v := uint16(n)
		out.Sequence = &v
	}
	b, _ := json.Marshal(out)
	if len(p.events) >= senderTraceEventLimit {
		return "event_limit", "event_limit"
	}
	if p.retainedBytes+len(b)+1 > senderTraceRetainedLimit {
		return "retained_limit", "retained_limit"
	}
	if p.rendererPID == nil {
		v := pid
		p.rendererPID = &v
	}
	p.producer[kind] = pair
	p.retainedBytes += len(b) + 1
	p.events = append(p.events, out)
	p.counts[kind]++
	return "", ""
}
func (p *senderTraceParser) markersPresent() bool {
	for _, k := range []string{"encode", "encoded", "enqueue", "process"} {
		if p.counts[k] == 0 {
			return false
		}
	}
	return true
}
