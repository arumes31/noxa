package main

import (
	"testing"
	"time"
)

func TestQueryResponseTimeoutCoversServerBudgets(t *testing.T) {
	for _, command := range []string{"rolechange data={}", "channelchange data={}"} {
		if got := queryResponseTimeout(command); got <= 40*time.Second || got > time.Minute {
			t.Errorf("%s: timeout %s must cover the operation and commit reply and remain bounded", command, got)
		}
	}
	for _, command := range []string{"rolelist", "rolemembers data={}", "accesscheck data={}", "channelquery data={}"} {
		if got := queryResponseTimeout(command); got <= 10*time.Second || got > 20*time.Second {
			t.Errorf("%s: timeout %s must cover the protected read and remain bounded", command, got)
		}
	}
	if got := queryResponseTimeout("login client_login_name=rolechange"); got != readTimeout {
		t.Fatalf("login unexpectedly received mutation timeout: %s", got)
	}
}
