//go:build windows

package edge

import (
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Fake native objects invoke the actual COM vtables through syscall, including
// LPWSTR* outputs and the by-value eight-byte EventRegistrationToken.
type senderCOMFakeCore struct {
	ICoreWebView2
	handler               *senderTraceHandler
	receiver              *senderCOMFakeReceiver
	method, params, event string
	callHR, receiverHR    uintptr
	calls                 int
	synchronous           bool
}
type senderCOMFakeReceiver struct {
	senderTraceReceiver
	handler             *senderTraceHandler
	token, removedToken int64
	refs, removes       int
	addHR, removeHR     uintptr
}
type senderCOMFakeArgs struct {
	senderTraceEventArgs
	value       string
	hr          uintptr
	getterCalls int
}

var senderCOMTestDuplicate = windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHStrDupW")

func senderCOMFakeCall(c *senderCOMFakeCore, method, params *uint16, h *senderTraceHandler) uintptr {
	c.calls++
	c.method = windows.UTF16PtrToString(method)
	c.params = windows.UTF16PtrToString(params)
	if senderTraceHRESULT(c.callHR) != nil {
		return c.callHR
	}
	c.handler = h
	_, _, _ = h.vtbl.AddRef.Call(uintptr(unsafe.Pointer(h)))
	if c.synchronous {
		value, _ := windows.UTF16PtrFromString(`{"sync":true}`)
		_, _, _ = h.vtbl.Invoke.Call(uintptr(unsafe.Pointer(h)), 0, uintptr(unsafe.Pointer(value)))
		_, _, _ = h.vtbl.Release.Call(uintptr(unsafe.Pointer(h)))
		c.handler = nil
	}
	return c.callHR
}
func senderCOMFakeGetReceiver(c *senderCOMFakeCore, name *uint16, out **senderTraceReceiver) uintptr {
	c.event = windows.UTF16PtrToString(name)
	*out = nil
	if senderTraceHRESULT(c.receiverHR) != nil {
		return c.receiverHR
	}
	c.receiver.refs++
	*out = &c.receiver.senderTraceReceiver
	return 0
}
func senderCOMFakeAdd(r *senderCOMFakeReceiver, h *senderTraceHandler, token *int64) uintptr {
	if senderTraceHRESULT(r.addHR) != nil {
		return r.addHR
	}
	r.handler = h
	_, _, _ = h.vtbl.AddRef.Call(uintptr(unsafe.Pointer(h)))
	*token = r.token
	return 0
}
func senderCOMFakeRemove(r *senderCOMFakeReceiver, token int64) uintptr {
	r.removes++
	r.removedToken = token
	if senderTraceHRESULT(r.removeHR) != nil {
		return r.removeHR
	}
	if r.handler != nil {
		_, _, _ = r.handler.vtbl.Release.Call(uintptr(unsafe.Pointer(r.handler)))
		r.handler = nil
	}
	return 0
}
func senderCOMFakeRemove64(r *senderCOMFakeReceiver, token uintptr) uintptr {
	return senderCOMFakeRemove(r, int64(token))
}
func senderCOMFakeRemove32(r *senderCOMFakeReceiver, low, high uintptr) uintptr {
	return senderCOMFakeRemove(r, int64(uint64(uint32(low))|uint64(uint32(high))<<32))
}
func senderCOMFakeReleaseReceiver(r *senderCOMFakeReceiver) uintptr {
	r.refs--
	return uintptr(r.refs)
}
func senderCOMFakeGetJSON(a *senderCOMFakeArgs, out **uint16) uintptr {
	a.getterCalls++
	value, _ := windows.UTF16FromString(a.value)
	// SHStrDupW returns CoTaskMem-owned UTF16 through an actual out pointer,
	// matching the WebView2 event getter without integer-to-pointer conversion.
	hr, _, _ := senderCOMTestDuplicate.Call(uintptr(unsafe.Pointer(&value[0])), uintptr(unsafe.Pointer(out)))
	runtime.KeepAlive(value)
	if senderTraceHRESULT(hr) != nil {
		return hr
	}
	// Also return allocated memory on failure, exercising ownership cleanup.
	return a.hr
}

var senderCOMFakeCoreVtbl = iCoreWebView2Vtbl{
	CallDevToolsProtocolMethod:       NewComProc(senderCOMFakeCall),
	GetDevToolsProtocolEventReceiver: NewComProc(senderCOMFakeGetReceiver),
}
var senderCOMFakeRemove64Proc = NewComProc(senderCOMFakeRemove64)
var senderCOMFakeRemove32Proc = NewComProc(senderCOMFakeRemove32)
var senderCOMFakeReceiverVtbl = senderTraceReceiverVtbl{
	_IUnknownVtbl: _IUnknownVtbl{Release: NewComProc(senderCOMFakeReleaseReceiver)},
	AddEvent:      NewComProc(senderCOMFakeAdd),
	RemoveEvent:   senderCOMFakeRemove64Proc,
}
var senderCOMFakeArgsVtbl = senderTraceEventArgsVtbl{GetParameterObjectAsJSON: NewComProc(senderCOMFakeGetJSON)}

func newTraceFakeCore() *senderCOMFakeCore {
	rv := senderCOMFakeReceiverVtbl
	if unsafe.Sizeof(uintptr(0)) == 4 {
		rv.RemoveEvent = senderCOMFakeRemove32Proc
	}
	return &senderCOMFakeCore{
		ICoreWebView2: ICoreWebView2{vtbl: &senderCOMFakeCoreVtbl},
		receiver:      &senderCOMFakeReceiver{senderTraceReceiver: senderTraceReceiver{vtbl: &rv}, token: 0x1122334455667788},
	}
}
func senderCOMTestRooted(h *senderTraceHandler) bool { _, ok := senderTraceRoots.Load(h); return ok }
func senderCOMTestRootCount() int {
	n := 0
	senderTraceRoots.Range(func(_, _ any) bool { n++; return true })
	return n
}
func senderCOMTestInvoke(h *senderTraceHandler, hr uintptr, text string) {
	value, _ := windows.UTF16PtrFromString(text)
	_, _, _ = h.vtbl.Invoke.Call(uintptr(unsafe.Pointer(h)), hr, uintptr(unsafe.Pointer(value)))
	runtime.KeepAlive(value)
}
func senderCOMTestEmit(r *senderCOMFakeReceiver, a *senderCOMFakeArgs) {
	_, _, _ = r.handler.vtbl.Invoke.Call(uintptr(unsafe.Pointer(r.handler)), 0, uintptr(unsafe.Pointer(a)))
}

func TestSenderTraceCOMMethodLifetimeAndUTF16(t *testing.T) {
	c := newTraceFakeCore()
	before := senderCOMTestRootCount()
	calls := 0
	var got string
	cancel, err := c.callSenderTraceMethod("Tracing.start", `{"categories":"webrtc"}`, func(value string, err error) {
		if err != nil {
			t.Errorf("completion: %v", err)
		}
		calls++
		got = value
	})
	if err != nil || cancel == nil {
		t.Fatalf("call: %v", err)
	}
	h := c.handler
	if h.refs.Load() != 1 || !senderCOMTestRooted(h) {
		t.Fatal("native reference not rooted")
	}
	if c.method != "Tracing.start" || c.params != `{"categories":"webrtc"}` {
		t.Fatal("UTF16 input ABI")
	}
	runtime.GC()
	want := `{"value":"` + strings.Repeat("\u00fc", 65536) + `\ud83d\ude00"}`
	senderCOMTestInvoke(h, 0, want)
	senderCOMTestInvoke(h, 0, "duplicate")
	if calls != 1 || got != want {
		t.Fatalf("completion calls=%d bytes=%d", calls, len(got))
	}
	cancel()
	cancel()
	if !senderCOMTestRooted(h) {
		t.Fatal("cancel prematurely released native reference")
	}
	_, _, _ = h.vtbl.Release.Call(uintptr(unsafe.Pointer(h)))
	if senderCOMTestRooted(h) || senderCOMTestRootCount() != before {
		t.Fatal("native Release leaked root")
	}
}

func TestSenderTraceCOMCanceledLateCompletionAndSynchronousCompletion(t *testing.T) {
	c := newTraceFakeCore()
	calls := 0
	cancel, err := c.callSenderTraceMethod("Tracing.end", `{}`, func(string, error) { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	h := c.handler
	cancel()
	senderCOMTestInvoke(h, 0, `{}`)
	if calls != 0 || !senderCOMTestRooted(h) {
		t.Fatal("cancel delivery/lifetime")
	}
	_, _, _ = h.vtbl.Release.Call(uintptr(unsafe.Pointer(h)))
	c.synchronous = true
	for range 100 {
		cancel, err = c.callSenderTraceMethod("Performance.getMetrics", `{}`, func(string, error) { calls++ })
		if err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	if calls != 100 {
		t.Fatalf("synchronous calls=%d", calls)
	}
}

func TestSenderTraceCOMHRESULTAndAllowlist(t *testing.T) {
	c := newTraceFakeCore()
	before := senderCOMTestRootCount()
	callback := func(string, error) { t.Error("unexpected completion") }
	c.callHR = 0x80004005 // Positive uintptr on x64, negative signed HRESULT.
	if cancel, err := c.callSenderTraceMethod("Tracing.start", `{}`, callback); err == nil || cancel != nil {
		t.Fatal("failed HRESULT accepted")
	}
	if senderCOMTestRootCount() != before {
		t.Fatal("failed call root leak")
	}
	for _, method := range []string{"Runtime.evaluate", "Tracing.stop", ""} {
		if _, err := c.callSenderTraceMethod(method, `{}`, callback); err == nil {
			t.Fatal("allowlist bypass")
		}
	}
	for _, params := range []string{"x\x00y", strings.Repeat("x", senderTraceMaxParamsBytes+1)} {
		if _, err := c.callSenderTraceMethod("Tracing.start", params, callback); err == nil {
			t.Fatal("invalid params accepted")
		}
	}
	if c.calls != 1 {
		t.Fatal("rejected call reached native API")
	}
	c.callHR = 1 // S_FALSE still SUCCEEDED.
	_, err := c.callSenderTraceMethod("Tracing.end", `{}`, func(_ string, err error) {
		if err == nil || !strings.Contains(err.Error(), "80004005") {
			t.Errorf("completion HRESULT: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	senderCOMTestInvoke(c.handler, 0x80004005, "private data must not appear in error")
	_, _, _ = c.handler.vtbl.Release.Call(uintptr(unsafe.Pointer(c.handler)))
}

func TestSenderTraceCOMQueryInterface(t *testing.T) {
	h := newSenderTraceHandler(&senderTraceCompletedVtbl, senderTraceCompletedIID, func(string, error) {})
	defer senderTraceRelease(h)
	for _, iid := range []windows.GUID{senderTraceIUnknown, senderTraceCompletedIID} {
		var out unsafe.Pointer
		hr, _, _ := h.vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
		if hr != 0 || out != unsafe.Pointer(h) || h.refs.Load() != 2 {
			t.Fatal("QI ABI/ref")
		}
		_, _, _ = h.vtbl.Release.Call(uintptr(out))
	}
	unknown := windows.GUID{Data1: 42}
	out := unsafe.Pointer(h)
	hr, _, _ := h.vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&unknown)), uintptr(unsafe.Pointer(&out)))
	if uint32(hr) != 0x80004002 || out != nil {
		t.Fatal("QI unknown did not clear output")
	}
	hr, _, _ = h.vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&unknown)), 0)
	if uint32(hr) != 0x80004003 {
		t.Fatal("QI null output")
	}
}

func TestSenderTraceCOMEventOutputTokenAndUnsubscribe(t *testing.T) {
	c := newTraceFakeCore()
	before := senderCOMTestRootCount()
	calls := 0
	var got string
	unsubscribe, err := c.subscribeSenderTraceEvent("Tracing.dataCollected", func(value string, err error) {
		if err != nil {
			t.Errorf("event: %v", err)
		}
		calls++
		got = value
	})
	if err != nil {
		t.Fatal(err)
	}
	h := c.receiver.handler
	if c.receiver.refs != 1 || h.refs.Load() != 1 || !senderCOMTestRooted(h) {
		t.Fatal("subscription ownership")
	}
	a := &senderCOMFakeArgs{senderTraceEventArgs: senderTraceEventArgs{vtbl: &senderCOMFakeArgsVtbl}, value: strings.Repeat("\u00fc", 256*1024)}
	senderCOMTestEmit(c.receiver, a)
	if a.getterCalls != 1 || got != a.value || calls != 1 {
		t.Fatal("LPWSTR* output or larger event lost")
	}
	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if c.receiver.removedToken != c.receiver.token || c.receiver.removes != 1 {
		t.Fatal("eight-byte token passed incorrectly")
	}
	if c.receiver.refs != 0 || senderCOMTestRooted(h) || senderCOMTestRootCount() != before {
		t.Fatal("unsubscribe ownership leak")
	}
}

func TestSenderTraceCOMEventFailuresAndRemovalRetry(t *testing.T) {
	c := newTraceFakeCore()
	calls := 0
	var lastErr error
	unsubscribe, err := c.subscribeSenderTraceEvent("Tracing.bufferUsage", func(_ string, err error) { calls++; lastErr = err })
	if err != nil {
		t.Fatal(err)
	}
	a := &senderCOMFakeArgs{senderTraceEventArgs: senderTraceEventArgs{vtbl: &senderCOMFakeArgsVtbl}, value: "private", hr: 0x80070057}
	senderCOMTestEmit(c.receiver, a)
	if calls != 1 || lastErr == nil || strings.Contains(lastErr.Error(), "private") {
		t.Fatal("event HRESULT handling")
	}
	senderCOMTestEmit(c.receiver, nil)
	if calls != 2 || lastErr == nil {
		t.Fatal("missing args not reported")
	}
	h := c.receiver.handler
	c.receiver.removeHR = 0x80004005
	if err := unsubscribe(); err == nil {
		t.Fatal("remove failure ignored")
	}
	senderCOMTestEmit(c.receiver, a)
	if calls != 2 || a.getterCalls != 1 || c.receiver.refs != 1 || !senderCOMTestRooted(h) {
		t.Fatal("failed removal lifetime/delivery")
	}
	c.receiver.removeHR = 0
	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if c.receiver.refs != 0 || senderCOMTestRooted(h) {
		t.Fatal("retry failed to release")
	}
}

func TestSenderTraceCOMEventAdmissionFailures(t *testing.T) {
	c := newTraceFakeCore()
	before := senderCOMTestRootCount()
	callback := func(string, error) { t.Error("unexpected callback") }
	if _, err := c.subscribeSenderTraceEvent("Network.requestWillBeSent", callback); err == nil {
		t.Fatal("event allowlist")
	}
	c.receiverHR = 0x80004005
	if _, err := c.subscribeSenderTraceEvent("Tracing.tracingComplete", callback); err == nil {
		t.Fatal("receiver HRESULT")
	}
	c.receiverHR = 0
	c.receiver.addHR = 0x80004005
	if _, err := c.subscribeSenderTraceEvent("Tracing.tracingComplete", callback); err == nil {
		t.Fatal("add HRESULT")
	}
	if c.receiver.refs != 0 || senderCOMTestRootCount() != before {
		t.Fatal("failed add leaked ownership")
	}
}

func TestSenderTraceCOMJSONBounds(t *testing.T) {
	if _, err := senderTraceJSON(nil); err == nil {
		t.Fatal("nil JSON accepted")
	}
	units := make([]uint16, senderTraceMaxJSONUnits+1)
	for i := range units {
		units[i] = 'x'
	}
	if _, err := senderTraceJSON(&units[0]); err == nil {
		t.Fatal("unterminated oversized JSON accepted")
	}
	value, _ := windows.UTF16PtrFromString("\u00fc\U0001f600")
	got, err := senderTraceJSON(value)
	if err != nil || got != "\u00fc\U0001f600" {
		t.Fatal("UTF16 surrogate decoding")
	}
}
