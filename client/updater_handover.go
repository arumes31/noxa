package main

// updater_handover.go moves a connected session to the updated process before
// the old one exits (handover-v1). The old process (A) starts the verified
// replacement (B) beside itself with the update readiness channel. B starts
// hidden, logs in with the session's resume token as a server-side standby,
// then signals A over that channel:
//
//	committing  A silences its microphone and playback, B commits
//	committed   the server moved the session; A quits, B shows its window
//	abandoned   B could not commit; A restores audio and restarts as before
//
// No key or credential crosses this channel; B uses the same device keys
// from the shared profile and the normal login. Any failure, or no final
// signal within updateHandoverWait, falls back to the restart path.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const updateHandoverEnv = "NOXA_UPDATE_HANDOVER"

// Handover signals from B to A.
const (
	handoverCommitting byte = 1
	handoverCommitted  byte = 2
	handoverAbandoned  byte = 3
)

// updateHandoverWait bounds the standby and commit after B's readiness: the
// server drops a standby after 20 seconds.
var updateHandoverWait = 30 * time.Second

// updateHandoverConfig travels from A to B in the environment (same user, not
// secret): what B needs to remove A's executable afterwards and to verify it
// can share A's browser.
type updateHandoverConfig struct {
	Version     int    `json:"version"`
	Previous    string `json:"previous"`
	OldHash     string `json:"old_hash"`
	BrowserArgs string `json:"browser_args"`
}

func (c updateHandoverConfig) oldHash() ([sha256.Size]byte, bool) {
	var hash [sha256.Size]byte
	raw, err := hex.DecodeString(c.OldHash)
	if err != nil || len(raw) != sha256.Size {
		return hash, false
	}
	copy(hash[:], raw)
	return hash, true
}

// --- old process (A) ---------------------------------------------------------

// handoverSilence defers the old session's "disconnected" events while B
// commits: the server closes A's connection then, and a cue or reconnect
// would only matter if the commit fails.
var handoverSilence struct {
	sync.Mutex
	active   bool
	deferred []*connManager
}

func beginHandoverSilence() {
	handoverSilence.Lock()
	handoverSilence.active = true
	handoverSilence.Unlock()
}

// endHandoverSilence stops deferring; replay delivers the deferred events.
func endHandoverSilence(replay bool) {
	handoverSilence.Lock()
	deferred := handoverSilence.deferred
	handoverSilence.active, handoverSilence.deferred = false, nil
	handoverSilence.Unlock()
	if replay {
		for _, m := range deferred {
			m.emit("disconnected", "")
		}
	}
}

// handoverSessionTaken reports whether a connection closed during the commit,
// which the server does when it moves the session to the new process.
func handoverSessionTaken() bool {
	handoverSilence.Lock()
	defer handoverSilence.Unlock()
	return len(handoverSilence.deferred) > 0
}

func deferHandoverDisconnect(m *connManager) bool {
	handoverSilence.Lock()
	defer handoverSilence.Unlock()
	if handoverSilence.active {
		handoverSilence.deferred = append(handoverSilence.deferred, m)
	}
	return handoverSilence.active
}

// updateHandoverPossible reports whether the active session can move: the
// server supports handover and the saved connection carries its token.
func (a *App) updateHandoverPossible(resume string) bool {
	if resume == "" || a.ctx == nil {
		return false
	}
	cm := a.cmLoad()
	if cm == nil || !cm.connected() {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.supportsHandover && cm.resumeToken != ""
}

// handOverUpdate tries the handover and reports whether the session moved.
// fallback is false only when the restart path must not run either (a
// replacement that could not be stopped).
func (a *App) handOverUpdate(exe string) (moved, fallback bool, err error) {
	committing := func(pid int) {
		beginHandoverSilence()
		allowUpdateForeground(pid)
		a.emitPlain("update_handover", "commit")
	}
	moved, fallback, err = launchHandoverClient(exe, committing)
	if moved {
		endHandoverSilence(false)
		return true, false, nil
	}
	endHandoverSilence(true)
	a.emitPlain("update_handover", "abandon")
	return false, fallback, err
}

// launchHandoverClient starts the verified replacement beside this process
// and serves its handover signals.
func launchHandoverClient(exe string, onCommitting func(pid int)) (moved, fallback bool, err error) {
	if !clientUpdateRecovery.TryLock() {
		return false, false, fmt.Errorf("another update operation is already running")
	}
	defer clientUpdateRecovery.Unlock()
	pending := clientUpdateRecovery.pending
	if pending == nil || pending.handoff {
		return false, true, fmt.Errorf("no verified update is waiting to restart")
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false, true, err
	}
	target, err := filepath.EvalSymlinks(pending.target)
	if err != nil || resolved != target {
		return false, true, fmt.Errorf("no verified update is waiting to restart")
	}
	if installed, err := hashUpdateExecutable(pending.target); err != nil || installed != pending.newHash {
		return false, true, fmt.Errorf("the installed update changed; refusing to launch it")
	}
	readiness, err := newUpdateReadiness()
	if err != nil {
		return false, true, err
	}
	defer readiness.close()
	config, err := json.Marshal(updateHandoverConfig{Version: 1, Previous: pending.previous,
		OldHash: hex.EncodeToString(pending.oldHash[:]), BrowserArgs: updateBrowserArguments()})
	if err != nil {
		return false, true, err
	}
	// #nosec G204 -- target is this process's OS-resolved executable whose verified update hash was rechecked above.
	cmd := exec.CommandContext(context.Background(), pending.target)
	cmd.Env = append(updateChildEnvironment(os.Environ(), readiness.address(), readiness.nonce), updateHandoverEnv+"="+string(config))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return false, true, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	stop := func(cause error) (bool, bool, error) {
		if stopErr := stopUpdatedClient(cmd.Process, exited); stopErr != nil {
			return false, false, fmt.Errorf("%w; could not stop the replacement: %v", cause, stopErr)
		}
		return false, true, cause
	}
	startup, cancel := context.WithTimeout(context.Background(), updateStartupTimeout)
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- readiness.wait(startup, cmd.Process.Pid) }()
	select {
	case <-startup.Done():
		return stop(fmt.Errorf("updated app did not become ready: %w", startup.Err()))
	case err := <-exited:
		return stop(fmt.Errorf("updated app exited before startup completed: %v", err))
	case err := <-ready:
		if err != nil {
			return stop(fmt.Errorf("updated app did not confirm startup: %w", err))
		}
	}
	ctx, cancelHandover := context.WithTimeout(context.Background(), updateHandoverWait)
	defer cancelHandover()
	switch serveUpdateHandover(ctx, readiness, cmd.Process.Pid, exited, onCommitting) {
	case handoverMoved:
		return true, false, nil
	case handoverUnconfirmed:
		// The final signal may be lost after a successful commit. Once the
		// server closed this session's connection, the session lives in the
		// new process (with the only valid token): leave it running.
		if handoverSessionTaken() {
			log.Print("update handover: no confirmation, but the server moved the session; leaving it to the new process")
			return true, false, nil
		}
	}
	return stop(errors.New("the updated app did not take over the session"))
}

// handoverOutcome is how a handover ended for the old process.
type handoverOutcome int

const (
	handoverKept        handoverOutcome = iota // abandoned, failed or never committed: keep the session
	handoverMoved                              // the new process confirmed the move
	handoverUnconfirmed                        // committing, then no final signal before the deadline
)

type handoverSignal struct {
	kind    byte
	handled chan struct{}
}

// serveUpdateHandover accepts the child's signals until a final one, the
// child's exit or ctx's end. A signal is acknowledged only after it was
// handled, so the child commits only once this process is silent.
func serveUpdateHandover(ctx context.Context, r *updateReadiness, childPID int, exited <-chan error, onCommitting func(pid int)) handoverOutcome {
	nonce, err := hex.DecodeString(r.nonce)
	if err != nil {
		return handoverKept
	}
	stop := context.AfterFunc(ctx, r.close)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	signals := make(chan handoverSignal)
	go func() {
		for {
			conn, err := r.listener.Accept()
			if err != nil {
				return
			}
			serveUpdateHandoverSignal(ctx, conn, nonce, childPID, signals, done)
			_ = conn.Close()
		}
	}()
	committing := false
	for {
		select {
		case <-ctx.Done():
			if committing {
				return handoverUnconfirmed
			}
			return handoverKept
		case <-exited:
			return handoverKept
		case signal := <-signals:
			outcome, final := handoverKept, false
			switch signal.kind {
			case handoverCommitting:
				if !committing {
					committing = true
					onCommitting(childPID)
				}
			case handoverCommitted:
				outcome, final = handoverMoved, true
			case handoverAbandoned:
				final = true
			}
			close(signal.handled)
			if final {
				return outcome
			}
		}
	}
}

// serveUpdateHandoverSignal reads one signal (the readiness challenge, the
// child's PID and the signal byte), waits until it was handled and then
// acknowledges it. Anything else is ignored.
func serveUpdateHandoverSignal(ctx context.Context, conn net.Conn, nonce []byte, childPID int, signals chan<- handoverSignal, done <-chan struct{}) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(updateReadyIOTimeout)); err != nil {
		return
	}
	var message [41]byte
	if _, err := io.ReadFull(conn, message[:]); err != nil ||
		subtle.ConstantTimeCompare(message[:32], nonce) != 1 ||
		binary.BigEndian.Uint64(message[32:40]) != uint64(childPID) { // #nosec G115 -- OS process IDs are positive.
		return
	}
	signal := handoverSignal{kind: message[40], handled: make(chan struct{})}
	if signal.kind < handoverCommitting || signal.kind > handoverAbandoned {
		return
	}
	select {
	case signals <- signal:
	case <-done:
		return
	}
	<-signal.handled
	_ = conn.SetDeadline(time.Now().Add(updateReadyIOTimeout))
	_, _ = conn.Write([]byte{1})
}

// sendUpdateHandoverSignal is B's side: one acknowledged signal to A.
func sendUpdateHandoverSignal(address, nonce string, processID int, kind byte) error {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.ParseUint(port, 10, 16)
	challenge, nonceErr := hex.DecodeString(nonce)
	if err != nil || host != "127.0.0.1" || portErr != nil || number == 0 || nonceErr != nil || len(challenge) != 32 || processID <= 0 {
		return fmt.Errorf("invalid update handover channel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateReadyIOTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(updateReadyIOTimeout)); err != nil {
		return err
	}
	var message [41]byte
	copy(message[:32], challenge)
	binary.BigEndian.PutUint64(message[32:40], uint64(processID)) // #nosec G115 -- positive process ID was checked above.
	message[40] = kind
	if _, err := conn.Write(message[:]); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if ack[0] != 1 {
		return fmt.Errorf("update handover signal was rejected")
	}
	return nil
}

// --- new process (B) ---------------------------------------------------------

type incomingUpdateHandover struct {
	config         updateHandoverConfig
	address, nonce string
	once           sync.Once
}

// incomingHandover is set when the old process started this one to take over
// its session. Read once at start, like the readiness challenge.
var incomingHandover = loadIncomingHandover(os.Getenv, startedAfterUpdate)

func loadIncomingHandover(getenv func(string) string, afterUpdate bool) *incomingUpdateHandover {
	raw := getenv(updateHandoverEnv)
	_ = os.Unsetenv(updateHandoverEnv)
	if raw == "" || !afterUpdate || len(raw) > 4096 {
		return nil
	}
	var config updateHandoverConfig
	if json.Unmarshal([]byte(raw), &config) != nil || config.Version != 1 {
		log.Print("update handover: invalid configuration; starting normally")
		return nil
	}
	if _, ok := config.oldHash(); !ok {
		return nil
	}
	return &incomingUpdateHandover{config: config, address: getenv(updateReadyAddressEnv), nonce: getenv(updateReadyNonceEnv)}
}

func (h *incomingUpdateHandover) signal(kind byte) error {
	return sendUpdateHandoverSignal(h.address, h.nonce, os.Getpid(), kind)
}

// abandon tells A to keep the session and ends this hidden process.
func (h *incomingUpdateHandover) abandon(a *App) {
	h.once.Do(func() {
		if err := h.signal(handoverAbandoned); err != nil {
			log.Printf("update handover: abandon signal: %v", err)
		}
		a.quitting.Store(true)
		if a.ctx != nil {
			wailsQuit(a.ctx)
		}
	})
}

// commit finishes on this side once the server moved the session: A quits,
// this window appears in its place, and A's executable is removed after A
// has exited.
func (h *incomingUpdateHandover) commit(a *App) {
	h.once.Do(func() {
		waitParent := holdUpdateParent()
		// The old process also detects the move on its own if this is lost.
		for attempt := 0; attempt < 3; attempt++ {
			err := h.signal(handoverCommitted)
			if err == nil {
				break
			}
			log.Printf("update handover: commit signal: %v", err)
		}
		showHandoverWindow(a)
		go guardCrash("update handover cleanup", func() {
			ctx, cancel := context.WithTimeout(context.Background(), updateHandoverTimeoutForCleanup)
			defer cancel()
			if err := waitParent(ctx); err != nil {
				log.Printf("update handover: previous process still running; its executable is retained: %v", err)
				return
			}
			// Hotkeys the old process held become available now.
			a.registerHotkeys()
			h.removePrevious()
		})
	})
}

const updateHandoverTimeoutForCleanup = 90 * time.Second

var showUpdateWindow = wailsRuntime.WindowShow

// showHandoverWindow shows this hidden window where the old one was.
func showHandoverWindow(a *App) {
	restoreUpdateWindow()
	if a.ctx != nil {
		showUpdateWindow(a.ctx)
	}
}

func (h *incomingUpdateHandover) removePrevious() {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		log.Printf("update handover: previous executable retained: %v", err)
		return
	}
	newHash, err := hashUpdateExecutable(exe)
	if err != nil {
		log.Printf("update handover: previous executable retained: %v", err)
		return
	}
	oldHash, _ := h.config.oldHash()
	backup, captureErr := captureUpdateBackup(exe, h.config.Previous, oldHash)
	finishUpdateBackupCleanup(exe, newHash, backup, captureErr)
}
