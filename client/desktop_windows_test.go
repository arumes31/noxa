//go:build windows

package main

import (
	"slices"
	"sync"
	"testing"
)

func TestRunWindowsDesktopLifecycle(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	trayQuit := make(chan struct{})

	runWindowsDesktop(windowsDesktopLifecycle{
		runWails: func(startTray func()) {
			record("wails-start")
			startTray()
			startTray() // idempotent
			record("wails")
		},
		runTray: func(ready chan<- struct{}) {
			record("tray-ready")
			close(ready)
			<-trayQuit
			record("tray-exit")
		},
		quitTray: func() {
			record("tray-quit")
			close(trayQuit)
		},
	})

	want := []string{"wails-start", "tray-ready", "wails", "tray-quit", "tray-exit"}
	if !slices.Equal(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestRunWindowsDesktopTrayFailure(t *testing.T) {
	wailsRan := false
	runWindowsDesktop(windowsDesktopLifecycle{
		runWails: func(startTray func()) { startTray(); wailsRan = true },
		runTray: func(ready chan<- struct{}) {
			// Simulates tray failing / exiting before initializing
		},
		quitTray: func() {},
	})
	if !wailsRan {
		t.Fatalf("wails did not run when tray setup failed to initialize")
	}
}

// A launch forwarded to the running instance never reaches OnStartup.
func TestRunWindowsDesktopForwardedLaunchStartsNoTray(t *testing.T) {
	trayRan, quitRan := false, false
	runWindowsDesktop(windowsDesktopLifecycle{
		runWails: func(func()) {},
		runTray:  func(chan<- struct{}) { trayRan = true },
		quitTray: func() { quitRan = true },
	})
	if trayRan || quitRan {
		t.Fatalf("tray ran=%v quit=%v for a launch that never started", trayRan, quitRan)
	}
}
