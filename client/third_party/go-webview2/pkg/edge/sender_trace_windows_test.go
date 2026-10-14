//go:build windows

package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSenderTraceAdapterRejectsUnrecognizedAndMalformedControl(t *testing.T) {
	view := &ICoreWebView2{}
	emitted := 0
	emit := func(string) { emitted++ }
	if HandleSenderTraceControl(view, "ordinary app message", emit) {
		t.Fatal("swallowed ordinary dispatcher message")
	}
	for _, m := range []string{SenderTraceControlPrefix + "{}", SenderTraceControlPrefix + strings.Repeat("x", 600), SenderTraceControlPrefix + `{"command":"Tracing.start","token":"` + strings.Repeat("a", 64) + `","scope":"opaque-random-scope"}`} {
		if !HandleSenderTraceControl(view, m, emit) {
			t.Fatal("reserved invalid message escaped to dispatcher")
		}
	}
	if emitted != 0 {
		t.Fatal("invalid control echoed")
	}
	CloseSenderTrace(nil)
}
func TestSenderTraceAdapterCancelArmAndExportExactOwner(t *testing.T) {
	traceArmMu.Lock()
	traceArm = nil
	traceArmMu.Unlock()
	senderTraceWindowsMu.Lock()
	senderTraceCurrent = nil
	senderTraceView = nil
	senderTraceWindowsMu.Unlock()
	t.Cleanup(func() {
		traceArmMu.Lock()
		traceArm = nil
		traceArmMu.Unlock()
		senderTraceWindowsMu.Lock()
		senderTraceCurrent = nil
		senderTraceView = nil
		senderTraceWindowsMu.Unlock()
	})
	token, err := ArmSenderTrace("opaque-random-scope")
	if err != nil {
		t.Fatal(err)
	}
	view := &ICoreWebView2{}
	other := &ICoreWebView2{}
	messages := []string{}
	emit := func(s string) { messages = append(messages, s) }
	command := func(cmd string) string {
		b, _ := json.Marshal(senderTraceCommand{Command: cmd, Token: token, Scope: "opaque-random-scope"})
		return SenderTraceControlPrefix + string(b)
	}
	HandleSenderTraceControl(view, command("stop"), emit)
	if len(messages) != 1 || !strings.Contains(messages[0], "canceled_before_start") {
		t.Fatal("stop-before-start not resolved")
	}
	HandleSenderTraceControl(other, command("export"), emit)
	if len(messages) != 1 {
		t.Fatal("foreign view read cached result")
	}
	senderTraceCurrent.closed = true
	HandleSenderTraceControl(view, command("export"), emit)
	if len(messages) != 2 || !strings.Contains(messages[1], `"complete":false`) {
		t.Fatal("explicit terminal export lost on scope close")
	}
	var exported senderTraceEnvelope
	if json.Unmarshal([]byte(messages[1]), &exported) != nil || exported.Record == nil || exported.Record.Events == nil {
		t.Fatal("empty failed export must contain events array, not null")
	}
	// Replaying Start after cancellation cannot invoke the nil COM vtable.
	HandleSenderTraceControl(view, command("start"), emit)
	if len(messages) != 2 {
		t.Fatal("consumed token restarted trace")
	}
}
func TestSenderTraceTimerDispatchOnlyOwnedIdentifier(t *testing.T) {
	c, f, clock, _, _ := traceTest(t)
	traceStart(t, c, f)
	*clock = clock.Add(60 * 1000000000)
	senderTraceWindowsMu.Lock()
	senderTraceTimers[12345] = c
	senderTraceWindowsMu.Unlock()
	t.Cleanup(func() { senderTraceWindowsMu.Lock(); delete(senderTraceTimers, 12345); senderTraceWindowsMu.Unlock() })
	senderTraceTimerProc(0, 0, 999, 0)
	if len(f.calls) != 1 {
		t.Fatal("foreign timer")
	}
	senderTraceTimerProc(0, 0, 12345, 0)
	if len(f.calls) != 2 || f.calls[1].method != "Tracing.end" {
		t.Fatal("owner timer did not stop")
	}
}

func TestSenderTraceAdapterScopeLossInvalidatesOwnedSession(t *testing.T) {
	c, f, _, _, _ := traceTest(t)
	traceStart(t, c, f)
	view := &ICoreWebView2{}
	senderTraceWindowsMu.Lock()
	oldC, oldV := senderTraceCurrent, senderTraceView
	senderTraceCurrent = c
	senderTraceView = view
	senderTraceWindowsMu.Unlock()
	t.Cleanup(func() {
		senderTraceWindowsMu.Lock()
		senderTraceCurrent = oldC
		senderTraceView = oldV
		senderTraceWindowsMu.Unlock()
	})
	command := SenderTraceControlPrefix + `{"command":"stop","token":"` + c.token + `","scope":"` + c.scope + `","scope_lost":true}`
	if !HandleSenderTraceControl(view, command, func(string) {}) || c.reason != "scope_lost" || len(f.calls) != 2 || f.calls[1].method != "Tracing.end" {
		t.Fatal("scope stop did not invalidate and end owned session")
	}
}

type senderTraceRetainFake struct {
	ICoreWebView2
	refs int
}

func senderTraceRetainAdd(v *senderTraceRetainFake) uintptr     { v.refs++; return uintptr(v.refs) }
func senderTraceRetainRelease(v *senderTraceRetainFake) uintptr { v.refs--; return uintptr(v.refs) }

var senderTraceRetainTestVtbl = iCoreWebView2Vtbl{_IUnknownVtbl: _IUnknownVtbl{AddRef: NewComProc(senderTraceRetainAdd), Release: NewComProc(senderTraceRetainRelease)}}

func TestSenderTraceRetainsCoreForLateCleanup(t *testing.T) {
	v := &senderTraceRetainFake{ICoreWebView2: ICoreWebView2{vtbl: &senderTraceRetainTestVtbl}, refs: 1}
	release := retainSenderTraceView(&v.ICoreWebView2)
	if release == nil || v.refs != 2 {
		t.Fatal("missing core ownership")
	}
	v.Release() // Chromium drops its own reference while native Start is pending.
	if v.refs != 1 {
		t.Fatal("late callback has no retained core")
	}
	release()
	release()
	if v.refs != 0 {
		t.Fatal("native core released more than once")
	}
	if retainSenderTraceView(nil) != nil || retainSenderTraceView(&ICoreWebView2{}) != nil {
		t.Fatal("invalid core retained")
	}
}
