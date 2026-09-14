package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplyAndRestartRequiresWindowContext(t *testing.T) {
	if got := (&App{}).ApplyAndRestart(); got == "" {
		t.Fatal("restart without a live application context succeeded")
	}
}

func TestApplyAndRestartExitsWithCloseToTrayEnabled(t *testing.T) {
	originalLaunch, originalQuit := restartLaunch, wailsQuit
	originalHide, originalMarkHidden := windowHide, windowMarkHidden
	t.Cleanup(func() {
		restartLaunch, wailsQuit = originalLaunch, originalQuit
		windowHide, windowMarkHidden = originalHide, originalMarkHidden
	})
	app := &App{ctx: context.Background(), settings: DefaultSettings()}
	app.settings.CloseToTray = true
	hides := 0
	windowHide = func(context.Context) { hides++ }
	windowMarkHidden = func() {}
	if !app.beforeClose(app.ctx) || hides != 1 {
		t.Fatal("ordinary window close did not hide to tray")
	}
	restartLaunch = func(string) error { return nil }
	quitPrevented := true
	wailsQuit = func(ctx context.Context) { quitPrevented = app.beforeClose(ctx) }
	if got := app.ApplyAndRestart(); got != "" {
		t.Fatalf("restart: %s", got)
	}
	if quitPrevented || hides != 1 {
		t.Fatal("close-to-tray intercepted update restart and kept the old process running")
	}
	if !app.settings.CloseToTray {
		t.Fatal("restart changed the user's close-to-tray preference")
	}
}

func TestFailedRestartPreservesCloseToTrayBehavior(t *testing.T) {
	originalLaunch, originalQuit := restartLaunch, wailsQuit
	originalHide, originalMarkHidden := windowHide, windowMarkHidden
	t.Cleanup(func() {
		restartLaunch, wailsQuit = originalLaunch, originalQuit
		windowHide, windowMarkHidden = originalHide, originalMarkHidden
	})
	app := &App{ctx: context.Background(), settings: DefaultSettings()}
	app.settings.CloseToTray = true
	windowHide = func(context.Context) {}
	windowMarkHidden = func() {}
	restartLaunch = func(string) error { return errors.New("launch failed") }
	wailsQuit = func(context.Context) { t.Error("failed launch quit the running application") }
	if got := app.ApplyAndRestart(); got == "" {
		t.Fatal("failed launch reported success")
	}
	if !app.beforeClose(app.ctx) {
		t.Fatal("failed restart disabled close-to-tray")
	}
}

func TestApplyAndRestartLaunchesBeforeQuitAndStopsOnLaunchError(t *testing.T) {
	originalLaunch, originalQuit := restartLaunch, wailsQuit
	t.Cleanup(func() {
		restartLaunch = originalLaunch
		wailsQuit = originalQuit
	})
	var order []string
	restartLaunch = func(string) error {
		order = append(order, "launch")
		return nil
	}
	wailsQuit = func(context.Context) { order = append(order, "quit") }
	if got := (&App{ctx: context.Background()}).ApplyAndRestart(); got != "" {
		t.Fatalf("restart: %s", got)
	}
	if len(order) != 2 || order[0] != "launch" || order[1] != "quit" {
		t.Fatalf("restart order = %v, want launch then quit", order)
	}

	quitCalled := false
	restartLaunch = func(string) error { return errors.New("injected launch failure") }
	wailsQuit = func(context.Context) { quitCalled = true }
	if got := (&App{ctx: context.Background()}).ApplyAndRestart(); got == "" {
		t.Fatal("restart reported success after launch failure")
	}
	if quitCalled {
		t.Fatal("restart quit after launch failure")
	}
}

func TestRestoreWindowOpacityRetriesUntilNativeWindowExists(t *testing.T) {
	originalInterval, originalApply := lifecyclePollInterval, windowOpacityApply
	t.Cleanup(func() {
		lifecyclePollInterval = originalInterval
		windowOpacityApply = originalApply
	})
	lifecyclePollInterval = time.Millisecond
	var calls atomic.Int32
	windowOpacityApply = func(int) error {
		if calls.Add(1) == 1 {
			return errors.New("window not ready")
		}
		return nil
	}
	done := make(chan struct{})
	a := &App{settings: DefaultSettings()}
	go func() {
		a.restoreWindowOpacity(context.Background())
		close(done)
	}()
	select {
	case <-done:
		if got := calls.Load(); got != 2 {
			t.Fatalf("opacity attempts = %d, want retry then success", got)
		}
	case <-time.After(time.Second):
		t.Fatal("opacity restore did not retry to success")
	}
}

func TestMinimizeWatcherUsesRisingEdgeAndShutdownCancelsPromptly(t *testing.T) {
	originalInterval := lifecyclePollInterval
	originalIsMin := windowIsMinimized
	originalUnmin := windowUnminimize
	originalHide := windowHide
	originalMarkHidden := windowMarkHidden
	t.Cleanup(func() {
		lifecyclePollInterval = originalInterval
		windowIsMinimized = originalIsMin
		windowUnminimize = originalUnmin
		windowHide = originalHide
		windowMarkHidden = originalMarkHidden
	})
	lifecyclePollInterval = time.Millisecond
	windowIsMinimized = func(context.Context) bool { return true }
	windowUnminimize = func(context.Context) {}
	var hides atomic.Int32
	windowHide = func(context.Context) { hides.Add(1) }
	windowMarkHidden = func() {}

	watchCtx, cancel := context.WithCancel(context.Background())
	a := &App{
		ctx:             context.Background(),
		lifecycleCancel: cancel,
		settings:        func() Settings { s := DefaultSettings(); s.MinimizeToTray = true; return s }(),
		hotkeys:         map[string]*hotkeyReg{},
		tabs:            map[string]*tabState{},
	}
	done := make(chan struct{})
	go func() {
		a.watchMinimized(watchCtx)
		close(done)
	}()
	deadline := time.After(time.Second)
	for hides.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("minimize rising edge did not hide window")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(10 * time.Millisecond)
	if got := hides.Load(); got != 1 {
		t.Fatalf("persistent minimized state hid %d times, want one rising-edge action", got)
	}
	a.shutdown(context.Background())
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("shutdown did not cancel window watcher promptly")
	}
}
