package edge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type traceFakeCall struct {
	method, params string
	callback       func(string, error)
}
type traceFakeTransport struct {
	calls         []traceFakeCall
	callbacks     map[string]func(string, error)
	unsub         int
	failSubscribe string
	callError     string
}

func (f *traceFakeTransport) call(m, p string, cb func(string, error)) (func(), error) {
	f.calls = append(f.calls, traceFakeCall{m, p, cb})
	if m == f.callError {
		return nil, errors.New("private detail")
	}
	return func() {}, nil
}
func (f *traceFakeTransport) subscribe(n string, cb func(string, error)) (func() error, error) {
	if n == f.failSubscribe {
		return nil, errors.New("private detail")
	}
	if f.callbacks == nil {
		f.callbacks = map[string]func(string, error){}
	}
	f.callbacks[n] = cb
	return func() error { f.unsub++; delete(f.callbacks, n); return nil }, nil
}
func traceTest(t *testing.T) (*senderTraceController, *traceFakeTransport, *time.Time, *[]string, *int) {
	t.Helper()
	clock := time.Unix(100, 0)
	f := &traceFakeTransport{}
	emits := []string{}
	released := 0
	c := newSenderTraceController(f, strings.Repeat("a", 64), "opaque-random-scope", func() time.Time { return clock }, func(s string) { emits = append(emits, s) }, func() { released++ })
	return c, f, &clock, &emits, &released
}

const traceMarkers = `{"value":[{"name":"VideoEncoder::Encode","cat":"webrtc","ph":"X","ts":100,"pid":1,"tid":2,"args":{"rtp_timestamp":1000,"private":"secret"}},{"name":"VCMEncodedFrameCallback::Encoded","cat":"webrtc","ph":"I","ts":102,"pid":1,"tid":3,"args":{"timestamp":1000}},{"name":"TaskQueuePacedSender::EnqueuePackets::Loop","cat":"disabled-by-default-webrtc","ph":"B","ts":103,"pid":1,"tid":4,"args":{"rtp_timestamp":1000,"sequence_number":3}},{"name":"TaskQueuePacedSender::MaybeProcessPackets","cat":"disabled-by-default-webrtc","ph":"B","ts":104,"pid":1,"tid":4}]}`

func traceStart(t *testing.T, c *senderTraceController, f *traceFakeTransport) {
	t.Helper()
	c.begin()
	if len(f.calls) != 1 || len(f.callbacks) != 3 {
		t.Fatal("not subscribed before start")
	}
	f.calls[0].callback("{}", nil)
}
func TestSenderTraceCompletionRequiresBothSignals(t *testing.T) {
	for _, firstEvent := range []bool{false, true} {
		t.Run(map[bool]string{false: "end-first", true: "complete-first"}[firstEvent], func(t *testing.T) {
			c, f, clock, emits, released := traceTest(t)
			traceStart(t, c, f)
			f.callbacks["Tracing.dataCollected"](traceMarkers, nil)
			*clock = clock.Add(60 * time.Second)
			c.tick()
			if len(f.calls) != 2 || f.calls[1].method != "Tracing.end" {
				t.Fatal("deadline did not end once")
			}
			c.tick()
			event := func() { f.callbacks["Tracing.tracingComplete"](`{"dataLossOccurred":false}`, nil) }
			if firstEvent {
				event()
			} else {
				f.calls[1].callback("{}", nil)
			}
			if c.terminal {
				t.Fatal("premature completion")
			}
			if firstEvent {
				f.calls[1].callback("{}", nil)
			} else {
				event()
			}
			if c.state != "completed" || *released != 1 || f.unsub != 3 {
				t.Fatalf("state %s release %d cleanup %d", c.state, *released, f.unsub)
			}
			c.publish(true)
			raw := (*emits)[len(*emits)-1]
			if strings.Contains(raw, "secret") || strings.Contains(raw, "pid") || strings.Contains(raw, "VideoEncoder") {
				t.Fatal("untrusted trace metadata escaped")
			}
			var result senderTraceEnvelope
			if json.Unmarshal([]byte(raw), &result) != nil || result.Record == nil || !result.Record.Complete || result.Record.ElapsedMS != 60000 || len(result.Record.Events) != 4 {
				t.Fatal(raw)
			}
		})
	}
}
func TestSenderTraceStopDuringStartAndLateSuccess(t *testing.T) {
	c, f, clock, _, released := traceTest(t)
	c.begin()
	c.stop("scope_lost")
	if len(f.calls) != 1 {
		t.Fatal("end before successful start")
	}
	*clock = clock.Add(11 * time.Second)
	c.tick()
	if !c.terminal || *released != 0 {
		t.Fatal("unconfirmed native start released")
	}
	f.calls[0].callback("{}", nil)
	if len(f.calls) != 2 || f.calls[1].method != "Tracing.end" {
		t.Fatal("late successful start orphaned")
	}
	if c.reason != "scope_lost" {
		t.Fatal(c.reason)
	}
}
func TestSenderTraceStartFailureAndSubscribeFailureCleanup(t *testing.T) {
	c, f, _, _, released := traceTest(t)
	f.failSubscribe = "Tracing.bufferUsage"
	c.begin()
	if len(f.calls) != 0 || f.unsub != 2 || *released != 1 || c.reason != "subscribe_failed" {
		t.Fatal("subscription cleanup failed")
	}
	c, f, _, _, released = traceTest(t)
	c.begin()
	f.calls[0].callback("", errors.New("secret"))
	if c.reason != "start_failed" || f.unsub != 3 || *released != 1 || len(f.calls) != 1 {
		t.Fatal("failed start attempted end")
	}
}
func TestSenderTraceInvalidCompletionAndBufferLimits(t *testing.T) {
	for _, tt := range []struct{ name, payload, reason string }{{"Tracing.bufferUsage", `{"percentFull":0.99}`, "buffer_full"}, {"Tracing.bufferUsage", `{"percentFull":null}`, "malformed_buffer_usage"}, {"Tracing.tracingComplete", `{}`, "malformed_completion"}, {"Tracing.tracingComplete", `{"dataLossOccurred":true}`, "data_loss"}, {"Tracing.dataCollected", `{"value":null}`, "malformed_data"}} {
		t.Run(tt.reason, func(t *testing.T) {
			c, f, _, _, _ := traceTest(t)
			traceStart(t, c, f)
			f.callbacks[tt.name](tt.payload, nil)
			if c.reason != tt.reason || len(f.calls) != 2 {
				t.Fatalf("%s calls%d", c.reason, len(f.calls))
			}
		})
	}
}
func TestSenderTraceTimeoutMissingMarkersAndOverrun(t *testing.T) {
	c, f, clock, _, released := traceTest(t)
	c.begin()
	*clock = clock.Add(10 * time.Second)
	c.tick()
	if c.reason != "start_timeout" || len(f.calls) != 1 {
		t.Fatal("startup deadline")
	}
	*clock = clock.Add(10 * time.Second)
	c.tick()
	if !c.terminal || *released != 0 {
		t.Fatal("unsafe timeout retry")
	}
	c, f, clock, _, _ = traceTest(t)
	traceStart(t, c, f)
	*clock = clock.Add(62 * time.Second)
	c.tick()
	if c.reason != "deadline_overrun" {
		t.Fatal("cooperative timer claimed hard bound")
	}
	c, f, _, _, _ = traceTest(t)
	traceStart(t, c, f)
	c.stop("")
	f.calls[1].callback("{}", nil)
	f.callbacks["Tracing.tracingComplete"](`{"dataLossOccurred":false}`, nil)
	if c.state != "failed" || c.reason != "missing_markers" {
		t.Fatal("empty trace claimed valid")
	}
}
func TestSenderTraceCloseSuppressesLateDelivery(t *testing.T) {
	c, f, _, emits, _ := traceTest(t)
	traceStart(t, c, f)
	before := len(*emits)
	c.close()
	f.calls[1].callback("{}", nil)
	f.callbacks["Tracing.tracingComplete"](`{"dataLossOccurred":false}`, nil)
	if len(*emits) != before || c.reason != "scope_lost" {
		t.Fatal("closed scope emitted")
	}
}
func TestSenderTraceFixedCommandsAndConfig(t *testing.T) {
	good := `{"command":"start","token":"` + strings.Repeat("a", 64) + `","scope":"opaque-random-scope"}`
	if _, ok := parseSenderTraceCommand(good); !ok {
		t.Fatal("valid command")
	}
	for _, s := range []string{good + `{}`, strings.Replace(good, "start", "Tracing.start", 1), strings.Replace(good, `"command"`, `"path":"private","command"`, 1), strings.Repeat("x", 513)} {
		if _, ok := parseSenderTraceCommand(s); ok {
			t.Fatal("accepted arbitrary input")
		}
	}
	var cfg map[string]any
	if json.Unmarshal([]byte(senderTraceStartParameters), &cfg) != nil {
		t.Fatal("config")
	}
	tc := cfg["traceConfig"].(map[string]any)
	if tc["recordMode"] != "recordUntilFull" || tc["traceBufferSizeInKb"] != float64(4096) || cfg["transferMode"] != "ReportEvents" {
		t.Fatal("unbounded config")
	}
}
func TestSenderTraceArmIsOneUseAndDoesNotReplaceActive(t *testing.T) {
	traceArmMu.Lock()
	traceArm = nil
	traceArmMu.Unlock()
	t.Cleanup(func() { traceArmMu.Lock(); traceArm = nil; traceArmMu.Unlock() })
	token, err := ArmSenderTrace("opaque-random-scope")
	if err != nil || len(token) != 64 {
		t.Fatal(err)
	}
	if _, err := ArmSenderTrace("different-random-scope"); err == nil {
		t.Fatal("replaced pending arm")
	}
	if consumeSenderTraceArm(token, "wrong") || !consumeSenderTraceArm(token, "opaque-random-scope") || consumeSenderTraceArm(token, "opaque-random-scope") {
		t.Fatal("capability use")
	}
	if _, err := ArmSenderTrace("different-random-scope"); err == nil {
		t.Fatal("replaced active trace")
	}
	releaseSenderTraceArm("wrong")
	if traceArm == nil {
		t.Fatal("foreign release")
	}
	releaseSenderTraceArm(token)
	if _, err := ArmSenderTrace("https://private.example"); err == nil {
		t.Fatal("nonopaque scope")
	}
	token, _ = ArmSenderTrace("opaque-random-scope")
	traceArmMu.Lock()
	traceArm.expires = time.Now().Add(-time.Second)
	traceArmMu.Unlock()
	if consumeSenderTraceArm(token, "opaque-random-scope") {
		t.Fatal("expired arm")
	}
}

func TestSenderTraceIgnoresUnownedEventsBeforeStartSuccess(t *testing.T) {
	c, f, _, _, _ := traceTest(t)
	c.begin()
	f.callbacks["Tracing.dataCollected"](traceMarkers, nil)
	f.callbacks["Tracing.tracingComplete"](`{"dataLossOccurred":false}`, nil)
	if len(c.parser.events) != 0 || len(f.calls) != 1 || c.reason != "" {
		t.Fatal("adopted an unrelated native trace before Start succeeded")
	}
	f.calls[0].callback("{}", nil)
	if c.state != "recording" {
		t.Fatal(c.state)
	}
}
func TestSenderTraceUnsubscribeFailureCannotAccumulateNewSessions(t *testing.T) {
	c, _, _, _, released := traceTest(t)
	c.unsub = []func() error{func() error { return errors.New("private") }}
	c.finish(true)
	if c.state != "failed" || c.reason != "unsubscribe_failed" || *released != 0 {
		t.Fatal("failed cleanup permitted another subscription set")
	}
	c.finish(true)
	if *released != 0 {
		t.Fatal("later completion bypassed failed cleanup")
	}
}

func TestSenderTraceScopeLossIsTrueOnlyAndStopOnly(t *testing.T) {
	base := `{"command":"stop","token":"` + strings.Repeat("a", 64) + `","scope":"opaque-random-scope","scope_lost":true}`
	if c, ok := parseSenderTraceCommand(base); !ok || string(c.ScopeLost) != "true" {
		t.Fatal("explicit scope loss rejected")
	}
	for _, raw := range []string{strings.Replace(base, "true", "false", 1), strings.Replace(base, "true", "null", 1), strings.Replace(base, "true", `"true"`, 1), strings.Replace(base, `"stop"`, `"start"`, 1), strings.Replace(base, `"stop"`, `"status"`, 1), strings.Replace(base, `"stop"`, `"export"`, 1)} {
		if _, ok := parseSenderTraceCommand(raw); ok {
			t.Fatal("scope loss accepted outside stop:true contract")
		}
	}
}

func TestSenderTraceLateEndAcknowledgementDoesNotConfirmFlush(t *testing.T) {
	c, f, clock, emits, released := traceTest(t)
	traceStart(t, c, f)
	f.callbacks["Tracing.dataCollected"](traceMarkers, nil)
	lateComplete := f.callbacks["Tracing.tracingComplete"]
	*clock = clock.Add(time.Second)
	c.stop("")
	*clock = clock.Add(10 * time.Second)
	c.tick()
	if !c.terminal || c.reason != "flush_timeout" || *released != 0 {
		t.Fatal("missing flush did not retain ownership")
	}
	c.publish(true)
	before := (*emits)[len(*emits)-1]
	*clock = clock.Add(20 * time.Second)
	f.calls[1].callback("{}", nil)
	if !c.endOK || c.completeEvent || *released != 0 {
		t.Fatal("late End acknowledgement was treated as completed native flush")
	}
	// The failed record is frozen; even an already queued event after teardown
	// cannot retroactively turn the bounded failed attempt into a success.
	lateComplete(`{"dataLossOccurred":false}`, nil)
	c.publish(true)
	if (*emits)[len(*emits)-1] != before || *released != 0 {
		t.Fatal("late callback changed the failed record or ownership")
	}
}
func TestSenderTraceLateFailedStartReleasesWithoutChangingFailure(t *testing.T) {
	c, f, clock, emits, released := traceTest(t)
	c.begin()
	*clock = clock.Add(10 * time.Second)
	c.tick()
	*clock = clock.Add(10 * time.Second)
	c.tick()
	if !c.terminal || *released != 0 {
		t.Fatal("startup timeout was not retained")
	}
	c.publish(true)
	before := (*emits)[len(*emits)-1]
	*clock = clock.Add(5 * time.Second)
	f.calls[0].callback("", errors.New("private native detail"))
	if *released != 1 || len(f.calls) != 1 {
		t.Fatal("definitively failed Start did not release safely")
	}
	c.publish(true)
	if (*emits)[len(*emits)-1] != before {
		t.Fatal("late failure replaced the failed record")
	}
}
func TestSenderTraceLateSuccessfulStartEndsButPreservesFailedRecord(t *testing.T) {
	c, f, clock, emits, released := traceTest(t)
	c.begin()
	*clock = clock.Add(10 * time.Second)
	c.tick()
	*clock = clock.Add(10 * time.Second)
	c.tick()
	c.publish(true)
	before := (*emits)[len(*emits)-1]
	*clock = clock.Add(5 * time.Second)
	f.calls[0].callback("{}", nil)
	if len(f.calls) != 2 || f.calls[1].method != "Tracing.end" || *released != 0 {
		t.Fatal("late Start cleanup was not bounded/owned")
	}
	c.publish(true)
	if (*emits)[len(*emits)-1] != before {
		t.Fatal("late cleanup rewrote failed export elapsed time")
	}
}
