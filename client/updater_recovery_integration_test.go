//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// All replacement/rollback operations run in a disposable copy, never the
// primary test executable or an installed noXa application.
func TestClientUpdateRecoveryIntegration(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("exercises the shipped Windows client's running-executable rename semantics")
	}
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	payload := buildStartupRecoveryProbe(t)
	for _, name := range []string{"ready", "exit", "timeout", "wrong nonce", "crash after ready", "changed installed", "changed backup"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "updater-recovery-copy.exe")
			if err := os.WriteFile(executable, originalBytes, 0o700); err != nil {
				t.Fatal(err)
			}
			payloadPath := filepath.Join(dir, "verified-test-payload.exe")
			if err := os.WriteFile(payloadPath, payload, 0o700); err != nil {
				t.Fatal(err)
			}
			data := []byte("settings, identity, and chat history are outside executable rollback\n")
			dataPath := filepath.Join(dir, "user-data.fixture")
			if err := os.WriteFile(dataPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestClientUpdateRecoveryHelperProcess$", "-test.v")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "NOXA_RECOVERY_TEST_CHILD="+executable,
				"NOXA_RECOVERY_TEST_ORIGINAL="+original, "NOXA_RECOVERY_TEST_PAYLOAD="+payloadPath,
				"NOXA_RECOVERY_TEST_CASE="+name)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated recovery: %v\n%s", err, out)
			}
			if got, err := os.ReadFile(dataPath); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("user data changed: %q, %v", got, err)
			}
			want := originalBytes
			if name == "ready" || name == "changed backup" {
				want = payload
			} else if name == "changed installed" {
				want = []byte("external replacement must not be overwritten")
			}
			if got, err := os.ReadFile(executable); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("installed executable is not the expected successful update/restored previous version: %v", err)
			}
		})
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, originalBytes) {
		t.Fatal("primary test executable changed")
	}
}

func TestClientUpdateRecoveryHelperProcess(t *testing.T) {
	expected := os.Getenv("NOXA_RECOVERY_TEST_CHILD")
	if expected == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(os.Getenv("NOXA_RECOVERY_TEST_ORIGINAL"))
	if err != nil {
		t.Fatal(err)
	}
	if exe != expected || os.SameFile(current, original) || filepath.Base(exe) != "updater-recovery-copy.exe" {
		t.Fatal("refusing replacement outside the disposable executable copy")
	}
	payload, err := os.ReadFile(os.Getenv("NOXA_RECOVERY_TEST_PAYLOAD"))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyClientUpdate(bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if err := applyClientUpdate(bytes.NewReader(payload)); err == nil {
		t.Fatal("a pending update was overwritten by a duplicate apply")
	}
	updateStartupTimeout = 600 * time.Millisecond
	updateStartupStability = 150 * time.Millisecond
	name := os.Getenv("NOXA_RECOVERY_TEST_CASE")
	if name == "changed installed" {
		if err := os.WriteFile(exe, []byte("external replacement must not be overwritten"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if name == "changed backup" {
		// The original image remains mapped. Moving it and substituting the
		// backup name exercises the tamper check without writing a running exe.
		backup := clientUpdateRecovery.pending.previous
		if err := os.Rename(backup, backup+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backup, []byte("not the previous executable"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	err = launchUpdatedClient(exe)
	if name == "ready" {
		if err != nil {
			t.Fatalf("healthy update failed: %v", err)
		}
		if clientUpdateRecovery.pending != nil {
			t.Fatal("successful restart retained a pending operation")
		}
		return
	}
	if err == nil {
		t.Fatal("unhealthy or modified replacement was accepted")
	}
	if strings.HasPrefix(name, "changed ") {
		if errors.Is(err, errUpdateRestored) {
			t.Fatal("tampered files must not be reported as restored")
		}
		return
	}
	if !errors.Is(err, errUpdateRestored) {
		t.Fatalf("old executable was not restored: %v", err)
	}
	if clientUpdateRecovery.pending != nil {
		t.Fatal("restored app cannot retry its update")
	}
	// This line is reached by the still-running previous app after rollback.
	// A second apply proves the restored, still-mapped target can be updated.
	if err := applyClientUpdate(bytes.NewReader(payload)); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if err := restoreFailedClientUpdate(clientUpdateRecovery.pending, errors.New("test retry cleanup")); !errors.Is(err, errUpdateRestored) {
		t.Fatalf("restore retry fixture: %v", err)
	}
}

func buildStartupRecoveryProbe(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	const code = `package main
import ("encoding/binary"; "encoding/hex"; "io"; "net"; "os"; "time")
func main() {
 mode := os.Getenv("NOXA_RECOVERY_TEST_CASE")
 if mode == "exit" || mode == "changed backup" { os.Exit(17) }
 if mode == "timeout" { time.Sleep(10*time.Second); return }
 nonce, err := hex.DecodeString(os.Getenv("NOXA_UPDATE_READY_NONCE")); if err != nil { os.Exit(18) }
 if mode == "wrong nonce" { nonce[0] ^= 1 }
 conn, err := net.DialTimeout("tcp4", os.Getenv("NOXA_UPDATE_READY_ADDRESS"), time.Second); if err != nil { os.Exit(19) }
 _ = conn.SetDeadline(time.Now().Add(time.Second))
 var message [40]byte; copy(message[:32], nonce); binary.BigEndian.PutUint64(message[32:], uint64(os.Getpid()))
 if _, err = conn.Write(message[:]); err != nil { os.Exit(20) }
 var ack [1]byte; _, _ = io.ReadFull(conn, ack[:]); _ = conn.Close()
 if mode == "crash after ready" { os.Exit(21) }
 time.Sleep(time.Second)
}
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "startup-probe.exe")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, source)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build startup probe: %v\n%s", err, out)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
