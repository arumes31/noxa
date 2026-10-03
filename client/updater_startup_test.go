package main

import (
	"context"
	"encoding/hex"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestConfirmUpdateStartupRejectsUntrustedEndpoint(t *testing.T) {
	for _, address := range []string{"example.com:1234", "127.0.0.2:1234", "127.0.0.1:0", "127.0.0.1:65536", "http://127.0.0.1:1234"} {
		t.Run(address, func(t *testing.T) {
			t.Setenv(updateReadyAddressEnv, address)
			t.Setenv(updateReadyNonceEnv, hex.EncodeToString(make([]byte, 32)))
			if err := (&App{}).ConfirmUpdateStartup(); err == nil {
				t.Fatal("accepted a startup callback outside the exact loopback endpoint")
			}
		})
	}
}

func TestConfirmUpdateStartupWithoutSupervisor(t *testing.T) {
	t.Setenv(updateReadyAddressEnv, "")
	t.Setenv(updateReadyNonceEnv, "")
	if err := (&App{}).ConfirmUpdateStartup(); err != nil {
		t.Fatalf("ordinary startup must not require an update supervisor: %v", err)
	}
}

func TestUpdateReadinessRejectsWrongNonceAndProcess(t *testing.T) {
	supervisor, err := newUpdateReadiness()
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	const expectedPID = 1234
	go func() { done <- supervisor.wait(ctx, expectedPID) }()
	for _, attempt := range []struct {
		name  string
		nonce string
		pid   int
	}{
		{name: "wrong nonce", nonce: hex.EncodeToString(make([]byte, 32)), pid: expectedPID},
		{name: "wrong process", nonce: supervisor.nonce, pid: expectedPID + 1},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			if err := confirmUpdateReadiness(supervisor.address(), attempt.nonce, attempt.pid); err == nil {
				t.Fatal("unauthenticated readiness was accepted")
			}
		})
	}
	if err := confirmUpdateReadiness(supervisor.address(), supervisor.nonce, expectedPID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUpdateReadinessCancelClosesBlockedConnection(t *testing.T) {
	supervisor, err := newUpdateReadiness()
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.wait(ctx, 1234) }()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp4", supervisor.address())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled startup wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("startup wait leaked while a client withheld its nonce")
	}
}

func TestUpdateChildEnvironmentReplacesInheritedHandshake(t *testing.T) {
	env := updateChildEnvironment([]string{"PATH=example", updateReadyAddressEnv + "=stale", updateReadyNonceEnv + "=stale"}, "127.0.0.1:"+strconv.Itoa(1234), "new")
	if len(env) != 3 || env[0] != "PATH=example" || env[1] != updateReadyAddressEnv+"=127.0.0.1:1234" || env[2] != updateReadyNonceEnv+"=new" {
		t.Fatalf("stale handshake was inherited: %v", env)
	}
}
