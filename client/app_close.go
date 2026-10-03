package main

import (
	"context"
	"time"
)

// A missing or stalled renderer must never leave the application unable to exit.
const closeNotificationTimeout = 4 * time.Second

// ReadyForCloseNotifications enables the goodbye handshake after the frontend
// has subscribed to app_closing. Closing during startup remains immediate.
func (a *App) ReadyForCloseNotifications() {
	a.closeMu.Lock()
	a.closeNotificationsReady = true
	a.closeMu.Unlock()
}

// requestClose returns whether Wails should keep the window alive. Explicit
// exits bypass close-to-tray, while all user exits share one goodbye handshake.
func (a *App) requestClose(ctx context.Context, explicit bool) bool {
	a.closeMu.Lock()
	if a.quitting.Load() {
		a.closeMu.Unlock()
		return false
	}
	if a.closePending {
		a.closeMu.Unlock()
		return true
	}
	if !explicit {
		a.settingsMu.Lock()
		closeToTray := a.settings.CloseToTray
		a.settingsMu.Unlock()
		if closeToTray {
			a.closeMu.Unlock()
			windowHide(ctx)
			windowMarkHidden()
			return true
		}
	}
	if !a.closeNotificationsReady || a.ctx == nil {
		a.closeMu.Unlock()
		return false
	}
	a.closePending = true
	a.closeTimer = time.AfterFunc(closeNotificationTimeout, a.CompleteClose)
	a.closeMu.Unlock()
	a.emitPlain("app_closing", nil)
	return true
}

// CompleteClose acknowledges finished (or unavailable) goodbye audio. The
// timeout calls the same path, so an acknowledgement racing it still exits once.
func (a *App) CompleteClose() {
	a.closeMu.Lock()
	if !a.closePending {
		a.closeMu.Unlock()
		return
	}
	a.closePending = false
	a.closeTimer.Stop()
	a.closeTimer = nil
	alreadyQuitting := a.quitting.Swap(true)
	a.closeMu.Unlock()
	if !alreadyQuitting {
		wailsQuit(a.ctx)
	}
}

func (a *App) cancelCloseNotification() {
	a.closeMu.Lock()
	a.quitting.Store(true)
	a.closePending = false
	if a.closeTimer != nil {
		a.closeTimer.Stop()
		a.closeTimer = nil
	}
	a.closeMu.Unlock()
}
