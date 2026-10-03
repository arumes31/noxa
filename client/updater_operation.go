package main

import (
	"context"
	"fmt"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// updateOperation serializes replacement of this application's executable.
// Cancellation and the transition to verification share the same lock.
type updateOperation struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	phase   string
	running bool
}

func (a *App) beginUpdate() (context.Context, func(), error) {
	a.update.mu.Lock()
	if a.update.running || a.update.phase == "ready" {
		a.update.mu.Unlock()
		return nil, nil, fmt.Errorf("an update is already processing or ready to restart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	a.update.cancel, a.update.running, a.update.phase = cancel, true, "downloading"
	a.update.mu.Unlock()
	a.emitUpdatePhase("downloading")
	return ctx, func() {
		cancel()
		a.update.mu.Lock()
		a.update.cancel, a.update.running = nil, false
		a.update.mu.Unlock()
	}, nil
}

func (a *App) updatePhase(ctx context.Context, phase string) error {
	a.update.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.update.mu.Unlock()
		return err
	}
	a.update.phase = phase
	a.update.mu.Unlock()
	a.emitUpdatePhase(phase)
	return nil
}

func (a *App) emitUpdatePhase(phase string) {
	if a.eventEmit != nil {
		a.eventEmit("update_phase", phase)
	} else if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "update_phase", phase)
	}
}

// CancelUpdate cancels only the download stage. Verification and executable
// replacement are deliberately not interruptible from the UI.
func (a *App) CancelUpdate() bool {
	a.update.mu.Lock()
	defer a.update.mu.Unlock()
	if !a.update.running || a.update.phase != "downloading" || a.update.cancel == nil {
		return false
	}
	a.update.cancel()
	return true
}

// GetUpdatePhase lets the restart UI distinguish a restored previous version
// from a launch error that can be retried against the same installed update.
func (a *App) GetUpdatePhase() string {
	a.update.mu.Lock()
	defer a.update.mu.Unlock()
	return a.update.phase
}
