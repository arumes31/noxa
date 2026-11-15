//go:build !windows

package main

import (
	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// runDesktop preserves the systray-required main-thread topology on macOS.
func runDesktop(app *App) {
	// recover is per-goroutine, so Wails carries its own guard (331).
	go guardCrash("wails", func() {
		runWails(app)
		systray.Quit()
	})

	initTray(app, nil)
}

func startWithWindowsEnabled() bool  { return false }
func setStartWithWindows(bool) error { return nil }
func registerDesktopIntegration()    {}

func tryAdoptSingleInstance(string, func(options.SecondInstanceData)) bool { return true }
