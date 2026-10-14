//go:build windows

package edge

import (
	"strings"
	"sync"
	"syscall"
	"time"
)

var senderTraceUser32 = syscall.NewLazyDLL("user32.dll")
var senderTraceSetTimer = senderTraceUser32.NewProc("SetTimer")
var senderTraceKillTimer = senderTraceUser32.NewProc("KillTimer")
var senderTraceTimerThunk = syscall.NewCallback(senderTraceTimerProc)
var senderTraceWindowsMu sync.Mutex
var senderTraceTimers = map[uintptr]*senderTraceController{}

// One bounded result cache, not a history of WebViews or raw trace records.
var senderTraceView *ICoreWebView2
var senderTraceCurrent *senderTraceController

type senderTraceCOMTransport struct{ view *ICoreWebView2 }

func (t senderTraceCOMTransport) call(method, params string, cb func(string, error)) (func(), error) {
	return t.view.callSenderTraceMethod(method, params, cb)
}
func (t senderTraceCOMTransport) subscribe(name string, cb func(string, error)) (func() error, error) {
	return t.view.subscribeSenderTraceEvent(name, cb)
}

// Hold one explicit interface reference through asynchronous Start/End cleanup.
// Unknown native completion deliberately retains this bounded reference.
func retainSenderTraceView(view *ICoreWebView2) func() {
	if view == nil || view.vtbl == nil || view.vtbl.AddRef == 0 || view.vtbl.Release == 0 {
		return nil
	}
	if view.AddRef() == 0 {
		return nil
	}
	released := false
	return func() {
		if !released {
			released = true
			view.Release()
		}
	}
}

func senderTraceTimerProc(_ uintptr, _ uint32, id uintptr, _ uint32) uintptr {
	senderTraceWindowsMu.Lock()
	c := senderTraceTimers[id]
	senderTraceWindowsMu.Unlock()
	if c != nil {
		c.tick()
	}
	return 0
}
func installSenderTraceTimer(c *senderTraceController) bool {
	id, _, _ := senderTraceSetTimer.Call(0, 0, 250, senderTraceTimerThunk)
	if id == 0 {
		return false
	}
	senderTraceWindowsMu.Lock()
	senderTraceTimers[id] = c
	senderTraceWindowsMu.Unlock()
	c.killTimer = func() {
		senderTraceWindowsMu.Lock()
		delete(senderTraceTimers, id)
		senderTraceWindowsMu.Unlock()
		senderTraceKillTimer.Call(0, id)
	}
	return true
}

// HandleSenderTraceControl must be called on the WebView owner STA ONLY AFTER
// the integration verifies the trusted top-level source. Reserved messages are
// consumed even when malformed. It exposes no arbitrary CDP method or path.
func HandleSenderTraceControl(view *ICoreWebView2, message string, emit func(string)) bool {
	if !strings.HasPrefix(message, SenderTraceControlPrefix) {
		return false
	}
	command, ok := parseSenderTraceCommand(strings.TrimPrefix(message, SenderTraceControlPrefix))
	if !ok || view == nil {
		return true
	}
	senderTraceWindowsMu.Lock()
	c := senderTraceCurrent
	owner := senderTraceView
	senderTraceWindowsMu.Unlock()
	if command.Command == "start" {
		if !consumeSenderTraceArm(command.Token, command.Scope) {
			return true
		}
		releaseView := retainSenderTraceView(view)
		c = newSenderTraceController(senderTraceCOMTransport{view}, command.Token, command.Scope, time.Now, emit, func() {
			if releaseView != nil {
				releaseView()
			}
			releaseSenderTraceArm(command.Token)
		})
		senderTraceWindowsMu.Lock()
		senderTraceCurrent = c
		senderTraceView = view
		senderTraceWindowsMu.Unlock()
		if releaseView == nil {
			c.fail("start_failed")
			c.finish(true)
			return true
		}
		if !installSenderTraceTimer(c) {
			c.fail("timer_failed")
			c.finish(true)
			return true
		}
		c.begin()
		return true
	}
	if c == nil || owner != view || c.token != command.Token || c.scope != command.Scope {
		// Cancel a still-armed capability without ever issuing Start. This handles a
		// disposed UI while its native Arm call was still resolving.
		if command.Command == "stop" && consumeSenderTraceArm(command.Token, command.Scope) {
			c = newSenderTraceController(senderTraceCOMTransport{view}, command.Token, command.Scope, time.Now, emit, func() { releaseSenderTraceArm(command.Token) })
			senderTraceWindowsMu.Lock()
			senderTraceCurrent = c
			senderTraceView = view
			senderTraceWindowsMu.Unlock()
			c.fail("canceled_before_start")
			c.finish(true)
		}
		return true
	}
	switch command.Command {
	case "stop":
		if !c.terminal {
			why := ""
			if len(command.ScopeLost) > 0 {
				why = "scope_lost"
			}
			c.stop(why)
		} else {
			c.publish(false)
		}
	case "status":
		c.publish(false)
	case "export":
		// Explicit terminal export survives scope shutdown, on the same trusted view.
		if c.terminal {
			wasClosed := c.closed
			oldEmit := c.emit
			c.closed = false
			c.emit = emit
			c.publish(true)
			c.closed = wasClosed
			c.emit = oldEmit
		}
	}
	return true
}

// CloseSenderTrace invalidates capture on navigation, renderer failure or
// shutdown. The caller must still be on the owner STA with a live WebView.
func CloseSenderTrace(view *ICoreWebView2) {
	if view == nil {
		return
	}
	senderTraceWindowsMu.Lock()
	c := senderTraceCurrent
	owner := senderTraceView
	senderTraceWindowsMu.Unlock()
	if c != nil && owner == view && !c.terminal {
		c.close()
	}
}
