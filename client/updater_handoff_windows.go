//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const updateSupervisorArgument = "--noxa-update-supervisor"

// Only disposable process integration fixtures shorten this bound.
var updateHandoffTimeout = 90 * time.Second

// Authenticated against the signed published v0.5.59 manifest. Compatibility
// is deliberately limited to this exact legacy executable, never a version
// string, environment flag, or an unverified backup selected by modification time.
const legacyUpdateExecutableSHA256 = "70bafbae4991ffd31c51927f3559dc20409ec519651294e96c04807041310ff6"

type updateProcessIdentity struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
}

type updateProcessHandle struct {
	identity updateProcessIdentity
	handle   windows.Handle
	image    string
}

func openUpdateProcess(identity updateProcessIdentity) (*updateProcessHandle, error) {
	if identity.PID == 0 {
		return nil, fmt.Errorf("missing update process identity")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, identity.PID)
	if err != nil {
		return nil, fmt.Errorf("open update process %d: %w", identity.PID, err)
	}
	closeOnError := func(err error) (*updateProcessHandle, error) { _ = windows.CloseHandle(handle); return nil, err }
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return closeOnError(err)
	}
	creation := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if identity.Created != 0 && identity.Created != creation {
		return closeOnError(fmt.Errorf("update process identity changed"))
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return closeOnError(err)
	}
	return &updateProcessHandle{identity: updateProcessIdentity{PID: identity.PID, Created: creation}, handle: handle, image: windows.UTF16ToString(buffer[:size])}, nil
}

func (p *updateProcessHandle) close() {
	if p != nil {
		_ = windows.CloseHandle(p.handle)
	}
}
func (p *updateProcessHandle) alive() bool {
	result, err := windows.WaitForSingleObject(p.handle, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}
func (p *updateProcessHandle) wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := windows.WaitForSingleObject(p.handle, 100)
		if err != nil {
			return err
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			return nil
		case uint32(windows.WAIT_TIMEOUT):
		default:
			return fmt.Errorf("unexpected process wait result %d", result)
		}
	}
}

func sameUpdatePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }
func updaterOwnedBackup(target, previous string) bool {
	name := filepath.Base(previous)
	return filepath.IsAbs(target) && filepath.IsAbs(previous) && !sameUpdatePath(target, previous) &&
		sameUpdatePath(filepath.Dir(target), filepath.Dir(previous)) && strings.HasPrefix(name, ".noxa-previous-") && strings.HasSuffix(name, ".exe")
}
func legacyUpdateHash(hash [sha256.Size]byte) bool {
	return hex.EncodeToString(hash[:]) == legacyUpdateExecutableSHA256
}

type updateSupervisorPlan struct {
	Version      int                    `json:"version"`
	Target       string                 `json:"target"`
	Previous     string                 `json:"previous"`
	OldHash      [sha256.Size]byte      `json:"oldHash"`
	NewHash      [sha256.Size]byte      `json:"newHash"`
	Parent       updateProcessIdentity  `json:"parent"`
	Browser      updateProcessIdentity  `json:"browser"`
	LegacyParent *updateProcessIdentity `json:"legacyParent,omitempty"`
	ArmedAddress string                 `json:"armedAddress"`
	ArmedNonce   string                 `json:"armedNonce"`
}

func (p updateSupervisorPlan) pending() *appliedClientUpdate {
	return &appliedClientUpdate{target: p.Target, previous: p.Previous, oldHash: p.OldHash, newHash: p.NewHash, retainFailedImage: true}
}
func (p updateSupervisorPlan) validate(executable string) error {
	if p.Version != 1 || !sameUpdatePath(executable, p.Target) || !updaterOwnedBackup(p.Target, p.Previous) {
		return fmt.Errorf("invalid updater-owned executable paths")
	}
	if p.Parent.PID == 0 || p.Parent.Created == 0 || p.Browser.PID == 0 || p.Browser.Created == 0 || p.Parent.PID == p.Browser.PID {
		return fmt.Errorf("invalid update handoff process identities")
	}
	if p.LegacyParent != nil && (p.LegacyParent.PID == 0 || p.LegacyParent.Created == 0 || p.LegacyParent.PID == p.Parent.PID || p.LegacyParent.PID == p.Browser.PID || !legacyUpdateHash(p.OldHash)) {
		return fmt.Errorf("invalid legacy update handoff identity")
	}
	return p.validateFiles()
}
func (p updateSupervisorPlan) validateFiles() error {
	target, err := hashUpdateExecutable(p.Target)
	if err != nil || target != p.NewHash {
		return fmt.Errorf("verified replacement executable changed")
	}
	previous, err := hashUpdateExecutable(p.Previous)
	if err != nil || previous != p.OldHash {
		return fmt.Errorf("verified previous executable changed")
	}
	return nil
}

type updateHandoffProcesses struct{ parent, browser, legacy *updateProcessHandle }

func (p *updateHandoffProcesses) close() { p.parent.close(); p.browser.close(); p.legacy.close() }
func (p updateSupervisorPlan) openProcesses() (*updateHandoffProcesses, error) {
	result := &updateHandoffProcesses{}
	fail := func(err error) (*updateHandoffProcesses, error) { result.close(); return nil, err }
	var err error
	result.parent, err = openUpdateProcess(p.Parent)
	if err != nil {
		return fail(err)
	}
	parentImage := p.Previous
	if p.LegacyParent != nil {
		parentImage = p.Target
	}
	if !result.parent.alive() || !sameUpdatePath(result.parent.image, parentImage) {
		return fail(fmt.Errorf("update parent image or lifetime changed"))
	}
	if p.LegacyParent != nil {
		result.legacy, err = openUpdateProcess(*p.LegacyParent)
		if err != nil {
			return fail(err)
		}
		hash, hashErr := hashUpdateExecutable(result.legacy.image)
		if !result.legacy.alive() || !updaterOwnedBackup(p.Target, result.legacy.image) || hashErr != nil || !legacyUpdateHash(hash) {
			return fail(fmt.Errorf("legacy parent no longer matches the authenticated release"))
		}
	}
	result.browser, err = openUpdateProcess(p.Browser)
	if err != nil {
		return fail(err)
	}
	if !result.browser.alive() || !strings.EqualFold(filepath.Base(result.browser.image), "msedgewebview2.exe") {
		return fail(fmt.Errorf("shared WebView browser identity is unavailable"))
	}
	return result, nil
}

// The supervisor is the verified replacement itself in a native-only mode.
// Its bounded plan arrives through the inherited anonymous pipe, never command
// strings or a file chosen from a directory. It never initializes Wails/profile.
func runUpdateSupervisorMode() (bool, error) {
	if len(os.Args) != 2 || os.Args[1] != updateSupervisorArgument {
		return false, nil
	}
	if dir, err := os.UserConfigDir(); err == nil {
		if writer, err := newDailyLogWriter(filepath.Join(dir, "noxa"), "client.log"); err == nil {
			log.SetOutput(writer)
			defer func() { _ = writer.Close() }()
		}
	}
	err := runUpdateSupervisor(os.Stdin)
	if err != nil {
		log.Printf("update supervisor failed: %v", err)
	}
	return true, err
}
func runUpdateSupervisor(input io.Reader) error {
	decoder := json.NewDecoder(io.LimitReader(input, 64*1024))
	decoder.DisallowUnknownFields()
	var plan updateSupervisorPlan
	if err := decoder.Decode(&plan); err != nil {
		return fmt.Errorf("read update handoff: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing update handoff data")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := plan.validate(executable); err != nil {
		return err
	}
	if plan.Parent.PID != uint32(os.Getppid()) {
		return fmt.Errorf("update handoff did not originate from the actual parent")
	} // #nosec G115 -- OS process IDs fit DWORD.
	processes, err := plan.openProcesses()
	if err != nil {
		return err
	}
	defer processes.close()
	// This separate native acknowledgement only promises that the helper holds
	// verified identities. It is never the frontend-readiness acknowledgement.
	if err := confirmUpdateReadiness(plan.ArmedAddress, plan.ArmedNonce, os.Getpid()); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateHandoffTimeout)
	defer cancel()
	if err := processes.parent.wait(ctx); err != nil {
		return fmt.Errorf("update parent did not exit; original process and backups retained: %w", err)
	}
	if processes.legacy != nil {
		if err := processes.legacy.wait(ctx); err != nil {
			return fmt.Errorf("legacy parent did not exit; its rollback ownership retained: %w", err)
		}
	}
	if err := plan.validateFiles(); err != nil {
		return err
	}
	if err := processes.browser.wait(ctx); err != nil {
		// Both original application hosts have exited. Restore rather than
		// leave an unstarted update installed; never terminate a shared browser.
		return restoreAndLaunchHandoff(plan, fmt.Errorf("shared WebView did not release the profile: %w", err))
	}
	clientUpdateRecovery.Lock()
	clientUpdateRecovery.pending = plan.pending()
	clientUpdateRecovery.Unlock()
	if err := launchUpdatedClient(plan.Target); err != nil {
		if errors.Is(err, errUpdateRestored) {
			if restoreErr := launchRestoredUpdate(plan.Target, plan.OldHash); restoreErr != nil {
				return fmt.Errorf("%w; restored app could not start: %v", err, restoreErr)
			}
			cleanupFailedSupervisorImage(plan)
			return fmt.Errorf("%w; original app relaunched and confirmed ready", err)
		}
		return err
	}
	log.Print("update supervisor: replacement frontend ready and stable after profile release")
	return nil
}
func restoreAndLaunchHandoff(plan updateSupervisorPlan, cause error) error {
	clientUpdateRecovery.Lock()
	restored := restoreFailedClientUpdate(plan.pending(), cause)
	clientUpdateRecovery.Unlock()
	if !errors.Is(restored, errUpdateRestored) {
		return restored
	}
	if err := launchRestoredUpdate(plan.Target, plan.OldHash); err != nil {
		return fmt.Errorf("%w; restored app could not start: %v", restored, err)
	}
	cleanupFailedSupervisorImage(plan)
	return fmt.Errorf("%w; original app relaunched and confirmed ready", restored)
}

// The restored app authenticates its actual native parent before Wails. Keep
// this running supervisor's renamed image readable until that initialization
// and real frontend acknowledgement have completed, then remove only its bytes.
func cleanupFailedSupervisorImage(plan updateSupervisorPlan) {
	path := plan.Previous + ".failed"
	file, err := os.Open(path)
	if err != nil {
		return
	}
	identity, statErr := file.Stat()
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	_ = file.Close()
	if statErr == nil && copyErr == nil && [sha256.Size]byte(hash.Sum(nil)) == plan.NewHash {
		removeVerifiedUpdateSnapshot(path, identity)
	}
}

type armedUpdateSupervisor struct {
	process *os.Process
	exited  <-chan error
}

func armUpdateSupervisor(plan updateSupervisorPlan) (*armedUpdateSupervisor, error) {
	readiness, err := newUpdateReadiness()
	if err != nil {
		return nil, err
	}
	defer readiness.close()
	plan.ArmedAddress, plan.ArmedNonce = readiness.address(), readiness.nonce
	if err := plan.validate(plan.Target); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- only the exact verified replacement is executable; the sole argument is fixed and the validated plan travels on stdin.
	cmd := exec.CommandContext(context.Background(), plan.Target, updateSupervisorArgument)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = updateChildEnvironment(os.Environ(), "", "")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- readiness.wait(ctx, cmd.Process.Pid) }()
	if err := awaitUpdatedClient(ctx, ready, exited); err != nil {
		if stopErr := stopUpdatedClient(cmd.Process, exited); stopErr != nil {
			return nil, fmt.Errorf("%w; could not stop owned supervisor: %v", err, stopErr)
		}
		return nil, err
	}
	return &armedUpdateSupervisor{process: cmd.Process, exited: exited}, nil
}

func launchClientUpdateAfterExit(executable string) error {
	if !clientUpdateRecovery.TryLock() {
		return fmt.Errorf("another update operation is already running")
	}
	defer clientUpdateRecovery.Unlock()
	pending := clientUpdateRecovery.pending
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if pending == nil || pending.handoff || !sameUpdatePath(resolved, pending.target) {
		return fmt.Errorf("no verified update is waiting to restart")
	}
	parent, err := openUpdateProcess(updateProcessIdentity{PID: uint32(os.Getpid())}) // #nosec G115 -- OS process IDs fit DWORD.
	if err != nil {
		return err
	}
	defer parent.close()
	browser, err := openUpdateProcess(updateProcessIdentity{PID: edge.UpdateBrowserProcessID()})
	if err != nil {
		return fmt.Errorf("identify existing WebView before restart: %w", err)
	}
	defer browser.close()
	plan := updateSupervisorPlan{Version: 1, Target: pending.target, Previous: pending.previous, OldHash: pending.oldHash, NewHash: pending.newHash, Parent: parent.identity, Browser: browser.identity}
	helper, err := armUpdateSupervisor(plan)
	if err != nil {
		return err
	}
	pending.handoff = true
	go func() {
		err := <-helper.exited
		clientUpdateRecovery.Lock()
		if clientUpdateRecovery.pending == pending {
			pending.handoff = false
		}
		clientUpdateRecovery.Unlock()
		if err != nil {
			log.Printf("update supervisor exited before this client closed: %v", err)
		}
	}()
	return nil
}

// The failed host may exit before its WebView browser releases the profile.
// Reap each exact owned failed child before a bounded retry, recheck the old
// executable every time, and never terminate a browser shared by other hosts.
func launchRestoredUpdate(target string, expected [sha256.Size]byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), updateHandoffTimeout)
	defer cancel()
	return retryRestoredUpdate(ctx, target, expected, waitRestoredUpdateRetry, launchRestoredUpdateAttempt)
}

func retryRestoredUpdate(ctx context.Context, target string, expected [sha256.Size]byte, pause func(context.Context, time.Duration) error, attempt func(context.Context, string) error) error {
	var last error
	for count := 0; count < 6; count++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restored startup deadline: %w; last attempt: %v", err, last)
		}
		hash, err := hashUpdateExecutable(target)
		if err != nil || hash != expected {
			return fmt.Errorf("restored executable changed; refusing relaunch")
		}
		if last = attempt(ctx, target); last == nil {
			return nil
		}
		var stillRunning *restoredUpdateStillRunningError
		if errors.As(last, &stillRunning) {
			return last
		}
		log.Printf("update restoration: owned startup attempt %d failed: %v", count+1, last)
		if count == 5 {
			break
		}
		if err := pause(ctx, time.Second<<uint(count)); err != nil {
			return fmt.Errorf("restored startup deadline: %w; last attempt: %v", err, last)
		}
	}
	return fmt.Errorf("restored startup failed after six bounded attempts: %w", last)
}

func waitRestoredUpdateRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func launchRestoredUpdateAttempt(parent context.Context, target string) error {
	readiness, err := newUpdateReadiness()
	if err != nil {
		return err
	}
	defer readiness.close()
	// #nosec G204 -- target is the authenticated original executable restored by the updater and hash checked by the caller immediately before each attempt.
	cmd := exec.CommandContext(context.Background(), target)
	cmd.Env = updateChildEnvironment(os.Environ(), readiness.address(), readiness.nonce)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	ctx, cancel := context.WithTimeout(parent, updateStartupTimeout)
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- readiness.wait(ctx, cmd.Process.Pid) }()
	if err := awaitUpdatedClient(ctx, ready, exited); err != nil {
		cancel()
		if stopErr := stopUpdatedClient(cmd.Process, exited); stopErr != nil {
			// A still-running owned child prevents another attempt. Preserve
			// that distinction so no second child races for the same profile.
			return &restoredUpdateStillRunningError{cause: err, stop: stopErr}
		}
		return err
	}
	return nil
}

type restoredUpdateStillRunningError struct{ cause, stop error }

func (e *restoredUpdateStillRunningError) Error() string {
	return fmt.Sprintf("%v; could not stop owned restored child: %v", e.cause, e.stop)
}

type legacyUpdateBootstrap struct {
	parent    *updateProcessHandle
	pending   *appliedClientUpdate
	mu        sync.Mutex
	helper    *armedUpdateSupervisor
	finishing atomic.Bool
}

// Set once before Wails starts and never cleared in this process: a temporary
// compatibility frontend may acknowledge readiness but may never become a normal
// session. Its successor starts with the ordinary hardware policy.
var legacyUpdateStartup *legacyUpdateBootstrap

func (*App) UpdateStartupPending() bool { return legacyUpdateStartup != nil }

func prepareLegacyUpdateBootstrap() error {
	if os.Getenv(updateReadyAddressEnv) == "" && os.Getenv(updateReadyNonceEnv) == "" {
		return nil
	}
	parent, err := openUpdateProcess(updateProcessIdentity{PID: uint32(os.Getppid())}) // #nosec G115 -- OS process IDs fit DWORD.
	if err != nil {
		return fmt.Errorf("identify update parent: %w", err)
	}
	retainParent := false
	defer func() {
		if !retainParent {
			parent.close()
		}
	}()
	hash, err := hashUpdateExecutable(parent.image)
	if err != nil {
		return fmt.Errorf("verify update parent: %w", err)
	}
	if !legacyUpdateHash(hash) {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if !parent.alive() || !updaterOwnedBackup(executable, parent.image) {
		return fmt.Errorf("legacy update parent does not own the previous executable")
	}
	newHash, err := hashUpdateExecutable(executable)
	if err != nil {
		return err
	}
	previous, identity, err := copyVerifiedUpdateBackup(parent.image, executable, hash)
	if err != nil {
		return err
	}
	if !parent.alive() {
		removeVerifiedUpdateSnapshot(previous, identity)
		return fmt.Errorf("legacy update parent exited during preparation")
	}
	if err := edge.SetLegacyUpdateCompatibility(); err != nil {
		removeVerifiedUpdateSnapshot(previous, identity)
		return err
	}
	legacyUpdateStartup = &legacyUpdateBootstrap{parent: parent, pending: &appliedClientUpdate{target: executable, previous: previous, oldHash: hash, newHash: newHash}}
	retainParent = true
	log.Print("update compatibility: authenticated v0.5.59 parent; preparing genuine temporary frontend before hardware-policy restart")
	return nil
}

func copyVerifiedUpdateBackup(source, target string, expected [sha256.Size]byte) (string, os.FileInfo, error) {
	if !updaterOwnedBackup(target, source) {
		return "", nil, fmt.Errorf("invalid authenticated legacy backup")
	}
	before, err := os.Lstat(source)
	if err != nil {
		return "", nil, err
	}
	if !before.Mode().IsRegular() {
		return "", nil, fmt.Errorf("legacy backup is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = input.Close() }()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", nil, fmt.Errorf("legacy backup identity changed")
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".noxa-previous-bootstrap-*.exe")
	if err != nil {
		return "", nil, err
	}
	name := output.Name()
	identity, err := output.Stat()
	if err != nil {
		_ = output.Close()
		return "", nil, err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			removeVerifiedUpdateSnapshot(name, identity)
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hash), input); err != nil {
		return "", nil, err
	}
	if err := output.Sync(); err != nil {
		return "", nil, err
	}
	if err := output.Close(); err != nil {
		return "", nil, err
	}
	after, err := os.Lstat(source)
	rechecked, hashErr := hashUpdateExecutable(source)
	copied, copyErr := hashUpdateExecutable(name)
	if err != nil || !os.SameFile(before, after) || [sha256.Size]byte(hash.Sum(nil)) != expected || hashErr != nil || rechecked != expected || copyErr != nil || copied != expected {
		return "", nil, fmt.Errorf("legacy executable changed while preserving rollback")
	}
	success = true
	return name, identity, nil
}
func removeVerifiedUpdateSnapshot(path string, identity os.FileInfo) {
	current, err := os.Lstat(path)
	if err == nil && identity != nil && current.Mode().IsRegular() && os.SameFile(identity, current) {
		_ = os.Remove(path)
	}
}

func prepareUpdateFrontendHandoff(*App) error {
	state := legacyUpdateStartup
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.helper != nil {
		return nil
	}
	if !state.parent.alive() {
		return fmt.Errorf("legacy update parent exited before genuine frontend readiness")
	}
	parent, err := openUpdateProcess(updateProcessIdentity{PID: uint32(os.Getpid())}) // #nosec G115 -- OS process IDs fit DWORD.
	if err != nil {
		return err
	}
	defer parent.close()
	browser, err := openUpdateProcess(updateProcessIdentity{PID: edge.UpdateBrowserProcessID()})
	if err != nil {
		return fmt.Errorf("identify compatibility WebView: %w", err)
	}
	defer browser.close()
	pending := state.pending
	identity := state.parent.identity
	plan := updateSupervisorPlan{Version: 1, Target: pending.target, Previous: pending.previous, OldHash: pending.oldHash, NewHash: pending.newHash, Parent: parent.identity, Browser: browser.identity, LegacyParent: &identity}
	helper, err := armUpdateSupervisor(plan)
	if err != nil {
		return err
	}
	state.helper = helper
	return nil
}
func completeUpdateFrontendHandoff(a *App) {
	state := legacyUpdateStartup
	if state == nil || state.finishing.Swap(true) {
		return
	}
	go func() {
		defer state.parent.close()
		ctx, cancel := context.WithTimeout(context.Background(), updateHandoffTimeout)
		defer cancel()
		if err := state.parent.wait(ctx); err != nil {
			log.Printf("legacy update parent did not exit after frontend readiness: %v", err)
			return
		}
		log.Print("update compatibility: original parent exited; closing temporary frontend for normal hardware-policy startup")
		a.quitting.Store(true)
		wailsQuit(a.ctx)
	}()
}
