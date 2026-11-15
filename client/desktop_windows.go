//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type windowsDesktopLifecycle struct {
	runWails func()
	runTray  func(chan<- struct{})
	quitTray func()
}

// runDesktop keeps Wails on the initial Windows OS thread. Wails' windowing
// backend records that thread during package initialisation and WebView2's COM
// apartment is thread-bound; moving wails.Run to a goroutine leaves its
// asynchronous environment callback without COM initialisation.
func runDesktop(app *App) {
	// A launch that Wails will forward to the running instance and exit
	// must not create a tray icon first: os.Exit would leave a ghost icon.
	if singleInstanceEnabled && singleInstanceRunning() {
		runWails(app)
		return
	}
	runWindowsDesktop(windowsDesktopLifecycle{
		runWails: func() { runWails(app) },
		runTray:  func(ready chan<- struct{}) { initTray(app, ready) },
		quitTray: systray.Quit,
	})
}

func runWindowsDesktop(lifecycle windowsDesktopLifecycle) {
	trayReady := make(chan struct{})
	trayDone := make(chan struct{})

	go func() {
		defer close(trayDone)
		var readyOnce sync.Once
		defer readyOnce.Do(func() { close(trayReady) })

		readyProxy := make(chan struct{})
		go func() {
			<-readyProxy
			readyOnce.Do(func() { close(trayReady) })
		}()

		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		guardCrash("tray", func() { lifecycle.runTray(readyProxy) })
	}()

	<-trayReady
	defer func() {
		lifecycle.quitTray()
		<-trayDone
	}()

	lifecycle.runWails()
}

// singleInstanceRunning probes the mutex Wails' SetupSingleInstance creates.
// shortcut: depends on Wails v2's "wails-app-<id>sim" name; a mismatch after a
// Wails upgrade only brings back a ghost tray icon on forwarded launches.
func singleInstanceRunning() bool {
	name, err := windows.UTF16PtrFromString("wails-app-" + singleInstanceID + "sim")
	if err != nil {
		return false
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}

// startupEntryMu serializes the toggle with the startup path refresh, so a
// refresh cannot write back an entry the user just removed.
var startupEntryMu sync.Mutex

func startWithWindowsEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer func() { _ = key.Close() }()
	_, _, err = key.GetStringValue(autostartValueName)
	return err == nil
}

func setStartWithWindows(enabled bool) error {
	startupEntryMu.Lock()
	defer startupEntryMu.Unlock()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open startup entry: %w", err)
	}
	defer func() { _ = key.Close() }()
	if !enabled {
		if err := key.DeleteValue(autostartValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("remove startup entry: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(autostartValueName, autostartCommand(exe))
}

// registerDesktopIntegration idempotently registers noxa:// for this exe and
// repoints an enabled startup entry that names another path.
func registerDesktopIntegration() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("desktop integration: %v", err)
		return
	}
	startupEntryMu.Lock()
	if key, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if current, _, err := key.GetStringValue(autostartValueName); err == nil && current != autostartCommand(exe) {
			if err := key.SetStringValue(autostartValueName, autostartCommand(exe)); err != nil {
				log.Printf("desktop integration: refresh startup entry: %v", err)
			}
		}
		_ = key.Close()
	}
	startupEntryMu.Unlock()
	for _, v := range []struct{ path, name, value string }{
		{protocolKey, "", protocolDescription},
		{protocolKey, "URL Protocol", ""},
		{protocolCommandKey, "", protocolCommand(exe)},
	} {
		if err := writeRegistryString(v.path, v.name, v.value); err != nil {
			log.Printf("desktop integration: %v", err)
			return
		}
	}
}

func writeRegistryString(path, name, value string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU\\%s: %w", path, err)
	}
	defer func() { _ = key.Close() }()
	if current, _, err := key.GetStringValue(name); err == nil && current == value {
		return nil
	}
	return key.SetStringValue(name, value)
}
