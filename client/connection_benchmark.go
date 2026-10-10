package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"noxa/internal/connectionbenchmark"
)

// ConnectionBenchmarkStatus is safe to expose to the renderer. Results contain
// timing only; the guest's credentials, address and ephemeral identity stay native.
type ConnectionBenchmarkStatus struct {
	ID             string                      `json:"id"`
	Phase          string                      `json:"phase"`
	ElapsedSeconds int                         `json:"elapsed_seconds"`
	Result         *connectionbenchmark.Result `json:"result,omitempty"`
	Error          string                      `json:"error,omitempty"`
}

type connectionBenchmarkOperation struct {
	mu     sync.Mutex
	tabID  string
	status ConnectionBenchmarkStatus
	cancel context.CancelFunc
	// A test seam avoids public network traffic in native lifecycle tests.
	run func(context.Context, connectionbenchmark.Options, func(connectionbenchmark.Progress)) (connectionbenchmark.Result, error)
}

// StartConnectionBenchmark starts one short synthetic self-echo test. A separate
// anonymous session leaves the user's current membership, microphone and voice
// transport untouched. Servers denying guests can safely reject this test.
func (a *App) StartConnectionBenchmark(tabID string) (ConnectionBenchmarkStatus, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return ConnectionBenchmarkStatus{}, err
	}
	cm.mu.Lock()
	if cm.conn == nil || cm.closed || !cm.tlsUsed || cm.fingerprint == "" {
		cm.mu.Unlock()
		return ConnectionBenchmarkStatus{}, errors.New("a connected, trusted TLS server tab is required")
	}
	opts := connectionbenchmark.Options{Address: cm.conn.RemoteAddr().String(), Fingerprint: cm.fingerprint}
	epoch := cm.connEpoch
	cm.mu.Unlock()
	a.tabsMu.Lock()
	if tab := a.tabs[tabID]; tab != nil && tab.cm == cm && tab.loginPasswords != nil {
		opts.ServerPassword = tab.loginPasswords.ServerPassword
	}
	a.tabsMu.Unlock()
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return ConnectionBenchmarkStatus{}, err
	}
	id := hex.EncodeToString(buf)
	op := &a.connectionBenchmark
	op.mu.Lock()
	if op.cancel != nil {
		op.mu.Unlock()
		return ConnectionBenchmarkStatus{}, errors.New("a connection test is already running")
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	status := ConnectionBenchmarkStatus{ID: id, Phase: "connecting"}
	op.tabID, op.status, op.cancel = tabID, status, cancel
	run := op.run
	if run == nil {
		run = connectionbenchmark.Run
	}
	op.mu.Unlock()
	go a.runConnectionBenchmark(ctx, cancel, cm, epoch, tabID, id, opts, run)
	return status, nil
}

func (a *App) runConnectionBenchmark(ctx context.Context, cancel context.CancelFunc, cm *connManager, epoch uint64, tabID, id string,
	opts connectionbenchmark.Options, run func(context.Context, connectionbenchmark.Options, func(connectionbenchmark.Progress)) (connectionbenchmark.Result, error)) {
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := a.requireTabCM(tabID)
				cm.mu.Lock()
				owned := cm.conn != nil && !cm.closed && cm.connEpoch == epoch
				cm.mu.Unlock()
				if err != nil || current != cm || !owned {
					cancel()
					return
				}
			}
		}
	}()
	op := &a.connectionBenchmark
	result, err := run(ctx, opts, func(progress connectionbenchmark.Progress) {
		op.mu.Lock()
		defer op.mu.Unlock()
		if op.status.ID == id {
			op.status.Phase, op.status.ElapsedSeconds = progress.Phase, progress.ElapsedSeconds
		}
	})
	cancelled := ctx.Err()
	cancel()
	<-watchDone
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.status.ID != id {
		return
	}
	op.cancel = nil
	switch {
	case cancelled != nil:
		op.status.Phase = "cancelled"
		if errors.Is(cancelled, context.DeadlineExceeded) {
			op.status.Phase = "failed"
			op.status.Error = "The connection test timed out."
		}
	case err != nil:
		op.status.Phase = "failed"
		// Do not expose raw transport errors, which can contain addresses.
		op.status.Error = "Connection test unavailable. This server must allow temporary guests and private Echo Test, with a reachable trusted media connection."
	default:
		op.status.Phase = "complete"
		op.status.Result = &result
	}
}

// ConnectionBenchmarkStatusForTab reads only the operation belonging to this tab.
func (a *App) ConnectionBenchmarkStatusForTab(tabID, id string) (ConnectionBenchmarkStatus, error) {
	op := &a.connectionBenchmark
	op.mu.Lock()
	defer op.mu.Unlock()
	if tabID == "" || id == "" || op.tabID != tabID || op.status.ID != id {
		return ConnectionBenchmarkStatus{}, errors.New("connection test is not available for this tab")
	}
	status := op.status
	if status.Result != nil {
		value := *status.Result
		status.Result = &value
	}
	return status, nil
}

// CancelConnectionBenchmark cancels the matching operation; it never affects voice.
func (a *App) CancelConnectionBenchmark(tabID, id string) error {
	op := &a.connectionBenchmark
	op.mu.Lock()
	defer op.mu.Unlock()
	if tabID == "" || id == "" || op.tabID != tabID || op.status.ID != id {
		return errors.New("connection test is not available for this tab")
	}
	if op.cancel != nil {
		op.cancel()
	}
	return nil
}

// Called by native shutdown, independently of renderer availability.
func (a *App) cancelConnectionBenchmark() {
	op := &a.connectionBenchmark
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.cancel != nil {
		op.cancel()
	}
}
