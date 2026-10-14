package edge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const SenderTraceControlPrefix = "__noxa_sender_trace_v1:"
const senderTraceStartParameters = `{"transferMode":"ReportEvents","bufferUsageReportingInterval":500,"traceConfig":{"recordMode":"recordUntilFull","traceBufferSizeInKb":4096,"includedCategories":["webrtc","disabled-by-default-webrtc"]}}`

var traceArmMu sync.Mutex
var traceArm *senderTraceArm

type senderTraceArm struct {
	token, scope string
	expires      time.Time
	consumed     bool
}

// ArmSenderTrace issues a one-use ten-second native arm. It does not start a
// trace. Scope is an opaque random identifier, never a URL, device or user ID.
func ArmSenderTrace(scope string) (string, error) {
	if len(scope) < 16 || len(scope) > 128 {
		return "", errors.New("invalid trace scope")
	}
	for _, r := range scope {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", errors.New("invalid trace scope")
		}
	}
	traceArmMu.Lock()
	defer traceArmMu.Unlock()
	if traceArm != nil && (traceArm.consumed || time.Now().Before(traceArm.expires)) {
		return "", errors.New("trace already armed or active")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("trace token unavailable")
	}
	token := hex.EncodeToString(b[:])
	traceArm = &senderTraceArm{token: token, scope: scope, expires: time.Now().Add(10 * time.Second)}
	return token, nil
}
func consumeSenderTraceArm(token, scope string) bool {
	traceArmMu.Lock()
	defer traceArmMu.Unlock()
	if traceArm == nil || traceArm.consumed || traceArm.token != token || traceArm.scope != scope || !time.Now().Before(traceArm.expires) {
		return false
	}
	traceArm.consumed = true
	return true
}
func releaseSenderTraceArm(token string) {
	traceArmMu.Lock()
	defer traceArmMu.Unlock()
	if traceArm != nil && traceArm.token == token {
		traceArm = nil
	}
}

type senderTraceCommand struct {
	Command   string          `json:"command"`
	Token     string          `json:"token"`
	Scope     string          `json:"scope"`
	ScopeLost json.RawMessage `json:"scope_lost,omitempty"`
}

func parseSenderTraceCommand(raw string) (senderTraceCommand, bool) {
	var c senderTraceCommand
	if len(raw) > 512 {
		return c, false
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || len(c.Token) != 64 || len(c.Scope) > 128 {
		return c, false
	}
	if _, err := hex.DecodeString(c.Token); err != nil {
		return c, false
	}
	if len(c.ScopeLost) > 0 && (c.Command != "stop" || string(c.ScopeLost) != "true") {
		return c, false
	}
	switch c.Command {
	case "start", "stop", "status", "export":
		return c, true
	}
	return c, false
}

type senderTraceTransport interface {
	call(string, string, func(string, error)) (func(), error)
	subscribe(string, func(string, error)) (func() error, error)
}
type senderTraceRecord struct {
	Schema           string                  `json:"schema"`
	Complete         bool                    `json:"complete"`
	Reason           string                  `json:"reason"`
	ElapsedMS        int64                   `json:"elapsed_ms"`
	TimerCooperative bool                    `json:"timer_cooperative"`
	MarkerCounts     map[string]int          `json:"marker_counts"`
	Events           []SenderTraceEvent      `json:"events"`
	Unknown          int                     `json:"dropped_unknown_events"`
	Diagnostics      *senderTraceDiagnostics `json:"parser_diagnostics,omitempty"`
}
type senderTraceEnvelope struct {
	Type   string             `json:"type"`
	Token  string             `json:"token"`
	Scope  string             `json:"scope"`
	State  string             `json:"state"`
	Reason string             `json:"reason"`
	Record *senderTraceRecord `json:"record,omitempty"`
}

// All controller operations and transport callbacks run on the owning STA.
// A pending native Start is never canceled: its late success must still End.
type senderTraceController struct {
	transport                                                                senderTraceTransport
	token, scope, state, reason                                              string
	now                                                                      func() time.Time
	emit                                                                     func(string)
	release                                                                  func()
	killTimer                                                                func()
	parser                                                                   senderTraceParser
	unsub                                                                    []func() error
	created, started, ended, flushDeadline                                   time.Time
	startPending, startedOK, endSent, endOK, completeEvent, terminal, closed bool
	cleanupFailed                                                            bool
}

func newSenderTraceController(t senderTraceTransport, token, scope string, now func() time.Time, emit func(string), release func()) *senderTraceController {
	return &senderTraceController{transport: t, token: token, scope: scope, state: "starting", now: now, emit: emit, release: release, created: now()}
}
func (c *senderTraceController) publish(export bool) {
	e := senderTraceEnvelope{Type: "noxa.sender_trace.v1", Token: c.token, Scope: c.scope, State: c.state, Reason: c.reason}
	if export && c.terminal {
		elapsed := int64(0)
		if !c.started.IsZero() && !c.ended.IsZero() {
			elapsed = c.ended.Sub(c.started).Milliseconds()
		}
		counts := map[string]int{}
		for _, k := range []string{"encode", "encoded", "enqueue", "process"} {
			counts[k] = c.parser.counts[k]
		}
		events := c.parser.events
		if events == nil {
			events = []SenderTraceEvent{}
		}
		e.Record = &senderTraceRecord{Schema: "noxa.sender_trace.numeric.v1", Complete: c.state == "completed", Reason: c.reason, ElapsedMS: elapsed, TimerCooperative: true, MarkerCounts: counts, Events: events, Unknown: c.parser.unknown, Diagnostics: c.parser.diagnostics}
	}
	if c.emit != nil && !c.closed {
		b, _ := json.Marshal(e)
		c.emit(string(b))
	}
}
func (c *senderTraceController) fail(reason string) {
	if c.reason == "" {
		c.reason = reason
	}
}
func (c *senderTraceController) begin() {
	for _, name := range []string{"Tracing.dataCollected", "Tracing.tracingComplete", "Tracing.bufferUsage"} {
		eventName := name
		unsub, err := c.transport.subscribe(name, func(raw string, err error) { c.event(eventName, raw, err) })
		if err != nil {
			c.fail("subscribe_failed")
			c.finish(true)
			return
		}
		c.unsub = append(c.unsub, unsub)
	}
	c.startPending = true
	c.started = c.now()
	_, err := c.transport.call("Tracing.start", senderTraceStartParameters, func(_ string, err error) {
		if !c.startPending {
			return
		}
		c.startPending = false
		if err != nil {
			c.fail("start_failed")
			c.finish(true)
			return
		}
		c.startedOK = true
		if c.closed || c.reason != "" || c.state == "stopping" || c.terminal {
			c.stop("")
			return
		}
		c.state = "recording"
		c.publish(false)
	})
	if err != nil && c.startPending {
		c.startPending = false
		c.fail("start_failed")
		c.finish(true)
		return
	}
	c.publish(false)
}
func (c *senderTraceController) stop(reason string) {
	if reason != "" {
		c.fail(reason)
	}
	if c.endSent {
		return
	}
	if !c.terminal {
		c.state = "stopping"
	}
	if c.flushDeadline.IsZero() {
		c.flushDeadline = c.now().Add(10 * time.Second)
	}
	if c.startPending {
		c.publish(false)
		return
	}
	if !c.startedOK {
		c.finish(true)
		return
	}
	c.endSent = true
	// Cleanup of a late successful Start must not rewrite a terminal export.
	if !c.terminal {
		c.ended = c.now()
	}
	_, err := c.transport.call("Tracing.end", "{}", func(_ string, err error) {
		if err != nil {
			c.fail("end_failed")
			c.finish(false)
			return
		}
		c.endOK = true
		c.maybeFinish()
	})
	if err != nil {
		c.fail("end_failed")
		c.finish(false)
		return
	}
	if !c.terminal {
		c.publish(false)
	}
}
func (c *senderTraceController) event(name, raw string, err error) {
	if c.terminal || !c.startedOK {
		return
	}
	if err != nil {
		c.stop("event_failed")
		return
	}
	switch name {
	case "Tracing.dataCollected":
		if why := c.parser.add(raw); why != "" {
			c.stop(why)
		}
	case "Tracing.bufferUsage":
		var v map[string]json.RawMessage
		if strictTraceJSON(raw, &v) != nil || v == nil {
			c.stop("malformed_buffer_usage")
			return
		}
		for _, k := range []string{"percentFull", "value"} {
			if value, found := v[k]; found {
				n, ok := traceNumber(value)
				if !ok || n > 1 {
					c.stop("malformed_buffer_usage")
					return
				}
				if n >= 0.98 {
					c.stop("buffer_full")
					return
				}
			}
		}
	case "Tracing.tracingComplete":
		var v struct {
			Loss   *bool           `json:"dataLossOccurred"`
			Stream json.RawMessage `json:"stream"`
		}
		if strictTraceJSON(raw, &v) != nil || v.Loss == nil || len(v.Stream) > 0 {
			c.stop("malformed_completion")
			return
		}
		c.completeEvent = true
		if *v.Loss {
			c.fail("data_loss")
		}
		if !c.endSent {
			c.stop("unexpected_completion")
		}
		c.maybeFinish()
	}
}
func (c *senderTraceController) maybeFinish() {
	if c.endOK && c.completeEvent {
		if !c.parser.markersPresent() {
			c.fail("missing_markers")
		}
		c.finish(true)
	}
}
func (c *senderTraceController) tick() {
	if c.terminal {
		return
	}
	now := c.now()
	if c.startPending && now.Sub(c.created) >= 10*time.Second {
		c.stop("start_timeout")
	}
	if c.startedOK && !c.endSent && now.Sub(c.started) >= 60*time.Second {
		why := ""
		if now.Sub(c.started) > 61500*time.Millisecond {
			why = "deadline_overrun"
		}
		c.stop(why)
	}
	if !c.flushDeadline.IsZero() && !now.Before(c.flushDeadline) {
		c.fail("flush_timeout")
		c.finish(false)
	}
}
func (c *senderTraceController) finish(safe bool) {
	if !c.terminal {
		c.terminal = true
		if c.reason == "" {
			c.state = "completed"
		} else {
			c.state = "failed"
		}
		if c.ended.IsZero() && !c.started.IsZero() {
			c.ended = c.now()
		}
		if c.killTimer != nil {
			c.killTimer()
			c.killTimer = nil
		}
		for _, f := range c.unsub {
			if err := f(); err != nil {
				c.fail("unsubscribe_failed")
				c.state = "failed"
				safe = false
				c.cleanupFailed = true
			}
		}
		c.unsub = nil
		c.publish(false)
	}
	// A timed-out pending Start or unconfirmed native flush retains the arm.
	// Tracing.end acknowledgement alone does not prove that trace buffers were
	// delivered: only tracingComplete confirms that. After terminal teardown we
	// do not revive event subscriptions or revise the failed export; the unknown
	// flush therefore intentionally prevents another attempt until restart.
	// A definitive late Start error is safe to release because no trace started.
	if safe && !c.cleanupFailed && c.release != nil {
		c.release()
		c.release = nil
	}
}
func (c *senderTraceController) close() { c.closed = true; c.stop("scope_lost") }
