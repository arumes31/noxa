package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/minio/selfupdate"
)

var errUpdateRestored = errors.New("the previous version was restored; check for updates to try again")

var clientUpdateRecovery struct {
	sync.Mutex
	pending *appliedClientUpdate
}

type appliedClientUpdate struct {
	target   string
	previous string
	oldHash  [sha256.Size]byte
	newHash  [sha256.Size]byte
}

// The test helper shortens these waits only inside a disposable process.
var updateStartupTimeout = 60 * time.Second
var updateStartupStability = time.Second

// applyClientUpdate receives only an asset already authenticated by the signed
// manifest, checksum, and executable-format checks in DownloadAndApply.
func applyClientUpdate(asset io.Reader) error {
	if !clientUpdateRecovery.TryLock() {
		return fmt.Errorf("another update operation is already running")
	}
	defer clientUpdateRecovery.Unlock()
	if clientUpdateRecovery.pending != nil {
		return fmt.Errorf("an installed update is already waiting to restart")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	oldHash, err := hashUpdateExecutable(exe)
	if err != nil {
		return fmt.Errorf("read previous executable: %w", err)
	}
	// Reserve a unique updater-owned name. Never overwrite an unrelated file
	// at a guessed fixed backup path, including a backup from an earlier run.
	previous, err := os.CreateTemp(filepath.Dir(exe), ".noxa-previous-*.exe")
	if err != nil {
		return fmt.Errorf("reserve previous executable: %w", err)
	}
	previousPath := previous.Name()
	if err := previous.Close(); err != nil {
		_ = os.Remove(previousPath)
		return err
	}
	newHash := sha256.New()
	if err := selfupdate.Apply(io.TeeReader(asset, newHash), selfupdate.Options{TargetPath: exe, OldSavePath: previousPath}); err != nil {
		if rollbackErr := selfupdate.RollbackError(err); rollbackErr != nil {
			return fmt.Errorf("install failed: %w; restore failed: %v; previous executable retained at %s", err, rollbackErr, previousPath)
		}
		// On normal failure selfupdate either never moved the old executable
		// or restored it; only our empty reservation may remain.
		if info, statErr := os.Stat(previousPath); statErr == nil && info.Size() == 0 {
			_ = os.Remove(previousPath)
		}
		return err
	}
	clientUpdateRecovery.pending = &appliedClientUpdate{
		target: exe, previous: previousPath, oldHash: oldHash,
		newHash: [sha256.Size]byte(newHash.Sum(nil)),
	}
	return nil
}

func hashUpdateExecutable(path string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	info, err := os.Lstat(path)
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() {
		return empty, fmt.Errorf("executable is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return empty, err
	}
	return [sha256.Size]byte(hash.Sum(nil)), nil
}

// launchUpdatedClient keeps this process alive until its exact replacement
// confirms native/frontend initialization. Only the child it starts can be
// terminated on failure; another client instance is never searched for/killed.
func launchUpdatedClient(exe string) error {
	if !clientUpdateRecovery.TryLock() {
		return fmt.Errorf("another update operation is already running")
	}
	defer clientUpdateRecovery.Unlock()
	pending := clientUpdateRecovery.pending
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if pending == nil || resolved != pending.target {
		return fmt.Errorf("no verified update is waiting to restart")
	}
	installedHash, err := hashUpdateExecutable(pending.target)
	if err != nil || installedHash != pending.newHash {
		return fmt.Errorf("the installed update changed; refusing to launch or replace it")
	}
	readiness, err := newUpdateReadiness()
	if err != nil {
		return err
	}
	defer readiness.close()
	// #nosec G204 -- target is this process's OS-resolved executable, and its verified update hash was rechecked above.
	// The healthy replacement outlives the startup deadline and this process.
	// Failure cleanup below explicitly terminates and waits for this owned child.
	cmd := exec.CommandContext(context.Background(), pending.target)
	cmd.Env = updateChildEnvironment(os.Environ(), readiness.address(), readiness.nonce)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return restoreFailedClientUpdate(pending, fmt.Errorf("updated app could not start: %w", err))
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
		close(exited)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), updateStartupTimeout)
	defer cancel()
	ready := make(chan error, 1)
	go func() { ready <- readiness.wait(ctx, cmd.Process.Pid) }()
	if err := awaitUpdatedClient(ctx, ready, exited); err != nil {
		cancel()
		if stopErr := stopUpdatedClient(cmd.Process, exited); stopErr != nil {
			return fmt.Errorf("%w; could not stop the replacement safely: %v; previous executable retained at %s", err, stopErr, pending.previous)
		}
		return restoreFailedClientUpdate(pending, err)
	}
	clientUpdateRecovery.pending = nil
	return nil
}

func awaitUpdatedClient(ctx context.Context, ready, exited <-chan error) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("updated app did not become ready: %w", ctx.Err())
	case err := <-exited:
		return fmt.Errorf("updated app exited before startup completed: %v", err)
	case err := <-ready:
		if err != nil {
			return fmt.Errorf("updated app did not confirm startup: %w", err)
		}
	}
	stable := time.NewTimer(updateStartupStability)
	defer stable.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("updated app startup timed out: %w", ctx.Err())
	case err := <-exited:
		return fmt.Errorf("updated app exited during startup: %v", err)
	case <-stable.C:
		return nil
	}
}

func stopUpdatedClient(process *os.Process, exited <-chan error) error {
	select {
	case <-exited:
		return nil
	default:
	}
	killErr := process.Kill()
	// Closing exited also covers the case where awaitUpdatedClient already
	// consumed the wait result. No process state is read concurrently with Wait.
	select {
	case <-exited:
		return nil
	case <-time.After(5 * time.Second):
		if killErr != nil {
			return killErr
		}
		return fmt.Errorf("replacement process did not exit within five seconds")
	}
}

func restoreFailedClientUpdate(pending *appliedClientUpdate, cause error) error {
	oldHash, oldErr := hashUpdateExecutable(pending.previous)
	newHash, newErr := hashUpdateExecutable(pending.target)
	if oldErr != nil || newErr != nil || oldHash != pending.oldHash || newHash != pending.newHash {
		return fmt.Errorf("%w; executable files changed, so automatic restoration was stopped; previous executable retained at %s", cause, pending.previous)
	}
	// Both names are updater-owned and on the executable's volume. Moving the
	// mapped old executable back is supported on Windows; no profile data moves.
	failedPath := pending.previous + ".failed"
	if _, err := os.Lstat(failedPath); !os.IsNotExist(err) {
		return fmt.Errorf("%w; recovery destination is unavailable; previous executable retained at %s", cause, pending.previous)
	}
	if err := os.Rename(pending.target, failedPath); err != nil {
		return fmt.Errorf("%w; could not move failed update: %v; previous executable retained at %s", cause, err, pending.previous)
	}
	if err := os.Rename(pending.previous, pending.target); err != nil {
		restoreErr := os.Rename(failedPath, pending.target)
		return fmt.Errorf("%w; could not restore previous executable: %v; reinstating update: %v; previous executable retained at %s", cause, err, restoreErr, pending.previous)
	}
	_ = os.Remove(failedPath)
	clientUpdateRecovery.pending = nil
	return fmt.Errorf("%w; %w", cause, errUpdateRestored)
}
