package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"noxa/internal/connectionbenchmark"
)

func benchmarkApp(t *testing.T) (*App, *connManager) {
	t.Helper()
	a, cm, _ := voiceDiagnosticApp(t, true)
	cm.tlsUsed, cm.fingerprint, cm.connEpoch = true, strings.Repeat("ab", 32), 1
	a.tabs["a"].loginPasswords = &loginPasswords{Password: "account-not-for-guest", ServerPassword: "native-only-server-secret"}
	return a, cm
}

func awaitBenchmark(t *testing.T, a *App, id string, phase string) ConnectionBenchmarkStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := a.ConnectionBenchmarkStatusForTab("a", id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Phase == phase {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("benchmark did not reach %s", phase)
	return ConnectionBenchmarkStatus{}
}

func TestConnectionBenchmarkIsBoundedScopedAndDoesNotChangeVoice(t *testing.T) {
	a, cm := benchmarkApp(t)
	entered := make(chan connectionbenchmark.Options, 1)
	a.connectionBenchmark.run = func(ctx context.Context, options connectionbenchmark.Options, _ func(connectionbenchmark.Progress)) (connectionbenchmark.Result, error) {
		entered <- options
		<-ctx.Done()
		return connectionbenchmark.Result{}, ctx.Err()
	}
	status, err := a.StartConnectionBenchmark("a")
	if err != nil {
		t.Fatal(err)
	}
	options := <-entered
	if options.ServerPassword != "native-only-server-secret" || options.Fingerprint != cm.fingerprint {
		t.Fatal("native connection scope not retained")
	}
	if _, err := a.StartConnectionBenchmark("a"); err == nil {
		t.Fatal("concurrent benchmark accepted")
	}
	if _, err := a.ConnectionBenchmarkStatusForTab("other", status.ID); err == nil {
		t.Fatal("cross-tab result exposed")
	}
	if err := a.CancelConnectionBenchmark("other", status.ID); err == nil {
		t.Fatal("cross-tab cancellation accepted")
	}
	if err := a.CancelConnectionBenchmark("a", status.ID); err != nil {
		t.Fatal(err)
	}
	awaitBenchmark(t, a, status.ID, "cancelled")
	if a.cmLoad() != cm || !cm.connected() || a.activeID != "a" {
		t.Fatal("benchmark changed existing connection")
	}
}

func TestConnectionBenchmarkStopsWhenOriginalConnectionChanges(t *testing.T) {
	a, cm := benchmarkApp(t)
	a.connectionBenchmark.run = func(ctx context.Context, _ connectionbenchmark.Options, _ func(connectionbenchmark.Progress)) (connectionbenchmark.Result, error) {
		<-ctx.Done()
		return connectionbenchmark.Result{}, ctx.Err()
	}
	status, err := a.StartConnectionBenchmark("a")
	if err != nil {
		t.Fatal(err)
	}
	cm.mu.Lock()
	cm.connEpoch++
	cm.mu.Unlock()
	awaitBenchmark(t, a, status.ID, "cancelled")
}

func TestConnectionBenchmarkRejectsPlaintextAndSanitizesFailure(t *testing.T) {
	a, cm := benchmarkApp(t)
	cm.tlsUsed = false
	if _, err := a.StartConnectionBenchmark("a"); err == nil {
		t.Fatal("plaintext connection accepted")
	}
	cm.tlsUsed = true
	a.connectionBenchmark.run = func(context.Context, connectionbenchmark.Options, func(connectionbenchmark.Progress)) (connectionbenchmark.Result, error) {
		return connectionbenchmark.Result{}, errors.New("private address or credential")
	}
	status, err := a.StartConnectionBenchmark("a")
	if err != nil {
		t.Fatal(err)
	}
	final := awaitBenchmark(t, a, status.ID, "failed")
	if strings.Contains(final.Error, "credential") || strings.Contains(final.Error, "private address") {
		t.Fatal("raw transport error exposed")
	}
}
