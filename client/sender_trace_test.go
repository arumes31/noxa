package main

import (
	"errors"
	"testing"
)

func TestSenderTraceArmRequiresExactAuthenticatedTab(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	calls := 0
	arm := func(scope string) (string, error) {
		calls++
		if scope != "opaque-test-scope" {
			t.Fatalf("wrong scope: %q", scope)
		}
		return "one-use-token", nil
	}
	if _, err := app.armSenderTraceForTab("missing", "opaque-test-scope", arm); err == nil {
		t.Fatal("missing tab fell back to another connection")
	}
	cm.clientID = ""
	if _, err := app.armSenderTraceForTab("a", "opaque-test-scope", arm); err == nil {
		t.Fatal("unauthenticated client armed trace")
	}
	if calls != 0 {
		t.Fatal("invalid requests reached native arm")
	}
	cm.clientID = "self"
	got, err := app.armSenderTraceForTab("a", "opaque-test-scope", arm)
	if err != nil || got != "one-use-token" || calls != 1 {
		t.Fatalf("arm = %q, %v, calls=%d", got, err, calls)
	}
	want := errors.New("already active")
	if _, err = app.armSenderTraceForTab("a", "opaque-test-scope", func(string) (string, error) { return "", want }); !errors.Is(err, want) {
		t.Fatalf("native refusal lost: %v", err)
	}
	if capture.data.Len() != 0 {
		t.Fatal("local trace arm sent network traffic")
	}
}
