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
func (p *senderTraceParser) add(raw string) string {
	if len(raw) > senderTraceMessageLimit {
		return "message_limit"
	}
	if p.rawBytes+len(raw) > senderTraceRawLimit {
		return "raw_limit"
	}
	p.rawBytes += len(raw)
	var chunk struct {
		Value []json.RawMessage `json:"value"`
	}
	if strictTraceJSON(raw, &chunk) != nil || chunk.Value == nil {
		return "malformed_data"
	}
	if len(chunk.Value) > senderTraceEventLimit {
		return "event_limit"
	}
	if p.counts == nil {
		p.counts = map[string]int{}
		p.producer = map[string][2]uint64{}
	}
	for _, entry := range chunk.Value {
		var e struct {
			Name string                     `json:"name"`
			Cat  string                     `json:"cat"`
			Ph   string                     `json:"ph"`
			TS   json.RawMessage            `json:"ts"`
			PID  json.RawMessage            `json:"pid"`
			TID  json.RawMessage            `json:"tid"`
			Args map[string]json.RawMessage `json:"args"`
		}
		if json.Unmarshal(entry, &e) != nil {
			return "malformed_data"
		}
		kind := ""
		field := ""
		switch e.Name {
		case "VideoEncoder::Encode":
			kind = "encode"
			field = "rtp_timestamp"
		case "VCMEncodedFrameCallback::Encoded":
			kind = "encoded"
			field = "timestamp"
		case "TaskQueuePacedSender::EnqueuePackets::Loop":
			kind = "enqueue"
			field = "rtp_timestamp"
		case "TaskQueuePacedSender::MaybeProcessPackets":
			kind = "process"
		default:
			p.unknown++
			continue
		}
		// Ignore end records and unsupported phases rather than invent paired spans.
		if (kind == "encoded" && e.Ph != "I" && e.Ph != "i") || (kind != "encoded" && e.Ph != "B" && e.Ph != "X") {
			p.unknown++
			continue
		}
		expected := "webrtc"
		if kind == "enqueue" || kind == "process" {
			expected = "disabled-by-default-webrtc"
		}
		if e.Cat != expected {
			p.unknown++
			continue
		}
		ts, ok := traceNumber(e.TS)
		if !ok {
			return "malformed_marker"
		}
		pid, pok := traceUint(e.PID, 9007199254740991)
		tid, tok := traceUint(e.TID, 9007199254740991)
		if !pok || !tok {
			return "malformed_marker"
		}
		if p.rendererPID != nil && *p.rendererPID != pid {
			return "ambiguous_marker_scope"
		}
		if p.rendererPID == nil {
			v := pid
			p.rendererPID = &v
		}
		pair := [2]uint64{pid, tid}
		if old, found := p.producer[kind]; found && old != pair {
			return "ambiguous_marker_scope"
		}
		p.producer[kind] = pair
		out := SenderTraceEvent{Kind: kind, TraceUS: ts}
		if field != "" {
			n, ok := traceUint(e.Args[field], math.MaxUint32)
			if !ok {
				return "malformed_marker"
			}
			v := uint32(n)
			out.RTP = &v
		}
		if kind == "enqueue" {
			n, ok := traceUint(e.Args["sequence_number"], math.MaxUint16)
			if !ok {
				return "malformed_marker"
			}
			v := uint16(n)
			out.Sequence = &v
		}
		b, _ := json.Marshal(out)
		if len(p.events) >= senderTraceEventLimit {
			return "event_limit"
		}
		if p.retainedBytes+len(b)+1 > senderTraceRetainedLimit {
			return "retained_limit"
		}
		p.retainedBytes += len(b) + 1
		p.events = append(p.events, out)
		p.counts[kind]++
	}
	return ""
}
func (p *senderTraceParser) markersPresent() bool {
	for _, k := range []string{"encode", "encoded", "enqueue", "process"} {
		if p.counts[k] == 0 {
			return false
		}
	}
	return true
}
