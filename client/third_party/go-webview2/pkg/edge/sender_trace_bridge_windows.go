//go:build windows

package edge

import (
	"net/url"
	"strings"
)

// Released Wails pages only. Development servers and embedded external pages
// cannot invoke this local native diagnostic control channel.
func trustedSenderTraceSource(source string) bool {
	u, err := url.Parse(source)
	return err == nil && u.Scheme == "http" && u.Host == "wails.localhost" &&
		u.User == nil && u.RawQuery == "" && (u.Path == "" || u.Path == "/")
}

func (e *Chromium) handleSenderTraceMessage(sender *ICoreWebView2, args *ICoreWebView2WebMessageReceivedEventArgs, message string) bool {
	if !strings.HasPrefix(message, "__noxa_sender_trace_v1:") {
		return false
	}
	// Consume this reserved channel even when validation fails. It must never
	// reach Wails' generic dispatcher or its message echo.
	if e.shuttingDown || sender == nil || sender != e.webview || args == nil {
		return true
	}
	source, err := args.GetSource()
	if err != nil || !trustedSenderTraceSource(source) {
		return true
	}
	top, err := sender.GetSource()
	if err != nil || !trustedSenderTraceSource(top) || source != top {
		return true
	}
	HandleSenderTraceControl(sender, message, func(payload string) {
		if !e.shuttingDown {
			_ = sender.PostWebMessageAsString(payload)
		}
	})
	return true
}
