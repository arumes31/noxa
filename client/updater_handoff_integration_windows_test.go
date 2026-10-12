//go:build windows && integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const handoffFixtureRootEnv = "NOXA_HANDOFF_FIXTURE_ROOT"
const handoffFixtureCaseEnv = "NOXA_HANDOFF_FIXTURE_CASE"
const handoffFixtureOldHashEnv = "NOXA_HANDOFF_FIXTURE_OLD_HASH"

func init() { updateHandoffTestMain = runHandoffFixtureMain }

// All code below is test-only. Normal app builds have neither this dispatch nor
// any environment switch bypassing production executable/browser validation.
func runHandoffFixtureMain() bool {
	dir := os.Getenv(handoffFixtureRootEnv)
	if dir == "" {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	if !filepath.IsAbs(dir) || !sameUpdatePath(filepath.Dir(executable), dir) || !strings.HasPrefix(filepath.Base(dir), "noxa-handoff-fixture-") {
		panic("fixture is outside its exclusively created directory")
	}
	switch filepath.Base(executable) {
	case "msedgewebview2.exe", ".noxa-previous-fixture.exe", "handoff-copy.exe":
	default:
		panic("refusing fixture dispatch in another executable")
	}
	updateStartupTimeout = 700 * time.Millisecond
	updateStartupStability = 100 * time.Millisecond
	updateHandoffTimeout = 1500 * time.Millisecond
	name := os.Getenv(handoffFixtureCaseEnv)
	if len(os.Args) == 2 && os.Args[1] == updateSupervisorArgument {
		output, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		log.SetOutput(output)
		err = runUpdateSupervisor(os.Stdin)
		message := "ready"
		status := 0
		if err != nil {
			message = err.Error()
			status = 1
		}
		_ = os.WriteFile(filepath.Join(dir, "supervisor-result.txt"), []byte(message), 0600)
		_ = output.Close()
		os.Exit(status)
	}
	if name == "ack parent" || name == "ack helper" {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, name+"-release")); err == nil {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return true
	}
	if filepath.Base(executable) == "msedgewebview2.exe" {
		duration := 600 * time.Millisecond
		if name == "browser timeout" {
			duration = 2 * time.Second
		}
		time.Sleep(duration)
		_ = os.WriteFile(filepath.Join(dir, "browser-exiting"), []byte("natural exit"), 0600)
		return true
	}
	if filepath.Base(executable) == ".noxa-previous-fixture.exe" {
		if err := runHandoffFixtureParent(dir, name); err != nil {
			panic(err)
		}
		return true
	}
	hash, err := hashUpdateExecutable(executable)
	if err != nil {
		panic(err)
	}
	restored := hex.EncodeToString(hash[:]) == os.Getenv(handoffFixtureOldHashEnv)
	if restored {
		if err := prepareLegacyUpdateBootstrap(); err != nil {
			panic(err)
		}
		// Simulate WebView's real refusal while a differently configured
		// browser still owns the profile. The production retry must reap us
		// before retrying, and must never kill this browser fixture.
		if name == "browser timeout" {
			if err := assertHandoffFixtureProcessesExited(dir, false); err != nil {
				os.Exit(23)
			}
		}
		if err := confirmUpdateReadiness(os.Getenv(updateReadyAddressEnv), os.Getenv(updateReadyNonceEnv), os.Getpid()); err != nil {
			panic(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "restored-ready"), []byte("genuine fixture ready"), 0600)
		time.Sleep(300 * time.Millisecond)
		return true
	}
	if err := assertHandoffFixtureProcessesExited(dir, true); err != nil {
		panic(err)
	}
	switch name {
	case "final exits":
		os.Exit(17)
	case "final timeout":
		time.Sleep(3 * time.Second)
		return true
	}
	if err := confirmUpdateReadiness(os.Getenv(updateReadyAddressEnv), os.Getenv(updateReadyNonceEnv), os.Getpid()); err != nil {
		panic(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "replacement-ready"), []byte("genuine fixture ready"), 0600)
	time.Sleep(300 * time.Millisecond)
	return true
}

func assertHandoffFixtureProcessesExited(dir string, parent bool) error {
	data, err := os.ReadFile(filepath.Join(dir, "identities.json"))
	if err != nil {
		return err
	}
	var identities []updateProcessIdentity
	if err := json.Unmarshal(data, &identities); err != nil {
		return err
	}
	start := 0
	if !parent {
		start = 1
	}
	for _, identity := range identities[start:] {
		process, err := openUpdateProcess(identity)
		if err != nil {
			continue
		}
		alive := process.alive()
		process.close()
		if alive {
			return fmt.Errorf("replacement started while exact old process %d still alive", identity.PID)
		}
	}
	return nil
}

func runHandoffFixtureParent(dir, name string) error {
	browserPath := filepath.Join(dir, "msedgewebview2.exe")
	browserCommand := exec.CommandContext(context.Background(), browserPath)
	browserCommand.Env = os.Environ()
	browserCommand.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := browserCommand.Start(); err != nil {
		return err
	}
	go func() { _ = browserCommand.Wait() }()
	browser, err := openUpdateProcess(updateProcessIdentity{PID: uint32(browserCommand.Process.Pid)})
	if err != nil {
		return err
	}
	defer browser.close()
	parent, err := openUpdateProcess(updateProcessIdentity{PID: uint32(os.Getpid())})
	if err != nil {
		return err
	}
	defer parent.close()
	identities, _ := json.Marshal([]updateProcessIdentity{parent.identity, browser.identity})
	if err := os.WriteFile(filepath.Join(dir, "identities.json"), identities, 0600); err != nil {
		return err
	}
	target := filepath.Join(dir, "handoff-copy.exe")
	if name == "target path alias" {
		// Keep an explicit alias in the handoff, as short-name Windows temp
		// directories can do. Do not pre-normalize away the regression.
		target = dir + string(os.PathSeparator) + "." + string(os.PathSeparator) + "handoff-copy.exe"
	}
	oldHash, err := hashUpdateExecutable(parent.image)
	if err != nil {
		return err
	}
	newHash, err := hashUpdateExecutable(target)
	if err != nil {
		return err
	}
	plan := updateSupervisorPlan{Version: 1, Target: target, Previous: parent.image, OldHash: oldHash, NewHash: newHash, Parent: parent.identity, Browser: browser.identity}
	helper, err := armUpdateSupervisor(plan)
	if err != nil {
		return err
	}
	helperIdentity, err := openUpdateProcess(updateProcessIdentity{PID: uint32(helper.process.Pid)})
	if err != nil {
		return err
	}
	helperBytes, _ := json.Marshal(helperIdentity.identity)
	helperIdentity.close()
	if err := os.WriteFile(filepath.Join(dir, "helper.json"), helperBytes, 0600); err != nil {
		return err
	}
	if name == "parent timeout" {
		select {
		case err := <-helper.exited:
			if err == nil {
				return fmt.Errorf("timed-out native helper incorrectly exited successfully")
			}
		case <-time.After(3 * time.Second):
			return fmt.Errorf("native helper did not honor parent wait deadline")
		}
	}
	return nil
}

func TestUpdateHandoffSupervisorIntegration(t *testing.T) {
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256(originalBytes)
	for _, name := range []string{"ready", "target path alias", "final exits", "final timeout", "browser timeout", "parent timeout"} {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "noxa-handoff-fixture-")
			if err != nil {
				t.Fatal(err)
			}
			// Deletion is restricted to this OS-created exact disposable root.
			t.Cleanup(func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			oldBytes := append(append([]byte(nil), originalBytes...), []byte("authenticated previous fixture")...)
			oldHash := sha256.Sum256(oldBytes)
			target, previous := filepath.Join(dir, "handoff-copy.exe"), filepath.Join(dir, ".noxa-previous-fixture.exe")
			for path, data := range map[string][]byte{target: originalBytes, previous: oldBytes, filepath.Join(dir, "msedgewebview2.exe"): originalBytes} {
				if err := os.WriteFile(path, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			dataPath := filepath.Join(dir, "user-profile.fixture")
			if err := os.WriteFile(dataPath, []byte("unchanged settings and profile"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, previous)
			cmd.Env = append(os.Environ(), handoffFixtureRootEnv+"="+dir, handoffFixtureCaseEnv+"="+name, handoffFixtureOldHashEnv+"="+hex.EncodeToString(oldHash[:]))
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("disposable parent: %v\n%s", err, out)
			}
			for {
				if _, err := os.Stat(filepath.Join(dir, "supervisor-result.txt")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			helperData, err := os.ReadFile(filepath.Join(dir, "helper.json"))
			if err != nil {
				t.Fatal(err)
			}
			var identity updateProcessIdentity
			if err := json.Unmarshal(helperData, &identity); err != nil {
				t.Fatal(err)
			}
			if helper, err := openUpdateProcess(identity); err == nil {
				if err := helper.wait(ctx); err != nil {
					t.Error(err)
				}
				helper.close()
			}
			result, err := os.ReadFile(filepath.Join(dir, "supervisor-result.txt"))
			if err != nil {
				t.Fatal(err)
			}
			logData, _ := os.ReadFile(filepath.Join(dir, "supervisor.log"))
			installed, err := hashUpdateExecutable(target)
			if err != nil {
				t.Fatal(err)
			}
			restored := name == "final exits" || name == "final timeout" || name == "browser timeout"
			want := originalHash
			if restored {
				want = oldHash
			}
			if installed != want {
				t.Fatalf("wrong installed bytes: result=%s log=%s", result, logData)
			}
			marker := "replacement-ready"
			if restored {
				marker = "restored-ready"
			}
			if name == "parent timeout" {
				if !strings.Contains(string(result), "parent did not exit") {
					t.Fatalf("unexpected timeout: %s", result)
				}
				if _, err := os.Stat(filepath.Join(dir, marker)); !os.IsNotExist(err) {
					t.Fatal("child launched before parent exit")
				}
			} else if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
				t.Fatalf("missing %s: result=%s log=%s", marker, result, logData)
			}
			if restored && !strings.Contains(string(result), "original app relaunched and confirmed ready") {
				t.Fatalf("restoration was not genuinely acknowledged: %s", result)
			}
			if (name == "ready" || name == "target path alias") && string(result) != "ready" {
				t.Fatalf("unexpected supervisor error: %s", result)
			}
			if data, err := os.ReadFile(dataPath); err != nil || !bytes.Equal(data, []byte("unchanged settings and profile")) {
				t.Fatal("profile fixture changed")
			}
			// A successful fixture child deliberately lives 300ms; allow its
			// natural exit before releasing the disposable directory.
			time.Sleep(350 * time.Millisecond)
			t.Logf("%s: exact parent/browser exit ordering and installed SHA verified; helper %s", name, strconv.FormatUint(uint64(identity.PID), 10))
		})
	}
	hash, err := hashUpdateExecutable(original)
	if err != nil || hash != originalHash {
		t.Fatal("primary test executable changed")
	}
}

func TestUpdateHandoffLostAcknowledgementIntegration(t *testing.T) {
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "noxa-handoff-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	start := func(name, mode string) (*exec.Cmd, <-chan error) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, contents, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(context.Background(), path)
		cmd.Env = append(os.Environ(), handoffFixtureRootEnv+"="+dir, handoffFixtureCaseEnv+"="+mode)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			_ = os.WriteFile(filepath.Join(dir, mode+"-release"), []byte("release"), 0600)
			select {
			case <-exited:
			case <-time.After(6 * time.Second):
				t.Error("owned fixture did not exit naturally")
			}
		})
		return cmd, exited
	}
	parent, parentExited := start(".noxa-previous-fixture.exe", "ack parent")
	helper, helperExited := start("msedgewebview2.exe", "ack helper")
	parentHandle, err := openUpdateProcess(updateProcessIdentity{PID: uint32(parent.Process.Pid)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, ".noxa-previous-snapshot.exe")
	if err := os.WriteFile(snapshot, []byte("owned rollback snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeState, beforeQuit := legacyUpdateStartup, wailsQuit
	t.Cleanup(func() { legacyUpdateStartup = beforeState; wailsQuit = beforeQuit })
	legacyUpdateStartup = &legacyUpdateBootstrap{parent: parentHandle, pending: &appliedClientUpdate{previous: snapshot}, helper: &armedUpdateSupervisor{process: helper.Process, exited: helperExited}}
	quit := make(chan struct{}, 1)
	wailsQuit = func(context.Context) { quit <- struct{}{} }
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	nonce := bytes.Repeat([]byte{42}, 32)
	t.Setenv(updateReadyAddressEnv, listener.Addr().String())
	t.Setenv(updateReadyNonceEnv, hex.EncodeToString(nonce))
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		var wire [40]byte
		_, err = io.ReadFull(conn, wire[:])
		if err == nil && (!bytes.Equal(wire[:32], nonce) || binary.BigEndian.Uint64(wire[32:]) != uint64(os.Getpid())) {
			err = fmt.Errorf("not the real readiness challenge")
		}
		// The parent accepted genuine readiness, but its ACK is lost before
		// the child can read it. Its independently owned process then exits.
		accepted <- err
	}()
	if err := (&App{ctx: context.Background()}).ConfirmUpdateStartup(); err == nil {
		t.Fatal("lost ACK was falsely reported as success")
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatal("ambiguous ACK deleted rollback snapshot")
	}
	select {
	case <-helperExited:
		t.Fatal("ambiguous ACK terminated the armed supervisor")
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "ack parent-release"), []byte("accepted parent exits"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted legacy parent exit did not close compatibility frontend")
	}
	if err := <-parentExited; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ack helper-release"), []byte("helper finishes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-helperExited; err != nil {
		t.Fatalf("retained helper did not exit naturally: %v", err)
	}
}
