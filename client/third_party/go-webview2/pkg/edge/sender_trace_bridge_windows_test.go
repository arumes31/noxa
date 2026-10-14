//go:build windows

package edge

import "testing"

func TestSenderTraceControlRequiresReleasedTopLevelOrigin(t *testing.T) {
	for _, source := range []string{"http://wails.localhost", "http://wails.localhost/", "http://wails.localhost/#chat"} {
		if !trustedSenderTraceSource(source) {
			t.Fatalf("released source rejected: %q", source)
		}
	}
	for _, source := range []string{"", "http://evil.example/", "http://wails.localhost.evil/", "http://wails.localhost@evil/", "http://evil@wails.localhost/", "http://wails.localhost:80/", "http://wails.localhost/embedded.html", "http://wails.localhost/?remote=1", "file:///wails.localhost", "http://localhost:5173/"} {
		if trustedSenderTraceSource(source) {
			t.Fatalf("untrusted source accepted: %q", source)
		}
	}
	e := &Chromium{}
	if !e.handleSenderTraceMessage(nil, nil, "__noxa_sender_trace_v1:{}") {
		t.Fatal("reserved invalid command leaked to generic dispatcher")
	}
	if e.handleSenderTraceMessage(nil, nil, "ordinary-wails-message") {
		t.Fatal("unrelated bridge message intercepted")
	}
}
