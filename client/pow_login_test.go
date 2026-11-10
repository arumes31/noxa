package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestLoginReasonRetryableKeepsRestartingServersInTheLoop(t *testing.T) {
	for reason, want := range map[string]bool{
		"internal error": true,
		"too many failed logins, try again later": true,
		netproto.RestartingReason(30):             true,
		"invalid credentials":                     false,
		netproto.ReasonIngressShieldUpdate:        false,
		"server is restarting; try again later":   false,
	} {
		if got := loginReasonRetryable(reason); got != want {
			t.Errorf("loginReasonRetryable(%q) = %v, want %v", reason, got, want)
		}
	}
}

func TestSolvePowChallengeAnswersTheServer(t *testing.T) {
	prefix := []byte("fedcba9876543210")
	nonce, ok := solvePowChallenge(netproto.PowChallenge{Algorithm: netproto.PowAlgorithm, Prefix: prefix, Difficulty: 14})
	if !ok || !netproto.PowSolutionValid(prefix, nonce, 14) {
		t.Fatalf("nonce=%d ok=%v", nonce, ok)
	}
	if _, ok := solvePowChallenge(netproto.PowChallenge{Algorithm: netproto.PowAlgorithm, Prefix: prefix, Difficulty: netproto.MaxPowDifficulty + 1}); ok {
		t.Fatal("oversized challenge solved")
	}
}

func TestShutdownHintReachesEveryTab(t *testing.T) {
	hint, ok := shutdownHint("tab-b", `{"type":"server_shutdown_scheduled","data":{"seconds":30,"reconnect_jitter":90}}`)
	if !ok || hint.TabID != "tab-b" || hint.Seconds != 30 || hint.ReconnectJitter != 60 {
		t.Fatalf("hint=%+v ok=%v", hint, ok)
	}
	for _, text := range []string{
		`{"type":"server_shutdown_scheduled","data":{"seconds":30}}`,
		`{"type":"chat","data":{"text":"\"server_shutdown_scheduled\""}}`,
		`not json "server_shutdown_scheduled"`,
	} {
		if _, ok := shutdownHint("tab-a", text); ok {
			t.Fatalf("hint from %s", text)
		}
	}
}
