// tray.go implements the system tray integration (287-290) via
// getlantern/systray — the one new client dependency this wave, justified
// because Wails v2 has no tray API.
//
// Threading is selected by the platform-specific desktop runner. On macOS
// systray must own the process main thread; on Windows it runs on a dedicated,
// locked OS thread so Wails/WebView2 can own the initial COM thread.
package main

import (
	"log"
	"sync"

	"github.com/getlantern/systray"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// tray is the shared tray controller (nil until initTray runs).
var trayCtl *tray

// tray wraps the systray menu and badge state.
type tray struct {
	app *App
	// emit is injected separately so tray menu actions can be tested
	// without constructing a Wails runtime context.
	emit func(string, any)

	mu        sync.Mutex
	status    trayStatus
	visible   bool
	publish   func(trayPresentation, bool)
	last      trayPresentation
	presented bool

	miShowHide   *systray.MenuItem
	miMute       *systray.MenuItem
	miDeafen     *systray.MenuItem
	miReconnect  *systray.MenuItem
	miDisconnect *systray.MenuItem
}

// initTray starts the tray on the calling goroutine and blocks until quit.
// When ready is non-nil it is closed after the tray menu is fully wired.
func initTray(a *App, ready chan<- struct{}) {
	trayCtl = &tray{app: a, emit: a.emitPlain, visible: true}
	systray.Run(func() {
		trayCtl.onReady()
		if ready != nil {
			close(ready)
		}
	}, func() { log.Printf("tray exit") })
}

// onReady builds the tray menu (called on the systray thread).
func (t *tray) onReady() {
	t.miShowHide = systray.AddMenuItem("Hide noXa", "show/hide the window")
	systray.AddSeparator()
	t.miMute = systray.AddMenuItem("Mute", "toggle microphone mute")
	t.miDeafen = systray.AddMenuItem("Deafen", "toggle deafen")
	systray.AddSeparator()
	t.miReconnect = systray.AddMenuItem("Reconnect last server", "reconnect the last server")
	t.miDisconnect = systray.AddMenuItem("Disconnect", "disconnect the active server tab")
	t.mu.Lock()
	t.publish = func(p trayPresentation, iconChanged bool) {
		if iconChanged {
			systray.SetIcon(trayIconFor(p.icon))
			if err := setTaskbarIcon(p.icon); err != nil {
				log.Printf("taskbar icon: %v", err)
			}
		}
		systray.SetTitle(p.title)
		systray.SetTooltip(p.tooltip)
		if t.status.muted {
			t.miMute.SetTitle("Unmute microphone")
		} else {
			t.miMute.SetTitle("Mute microphone")
		}
		if t.status.deafened {
			t.miDeafen.SetTitle("Unmute audio")
		} else {
			t.miDeafen.SetTitle("Mute audio")
		}
	}
	t.mu.Unlock()
	t.setConnected(t.app.Connected())
	miQuit := systray.AddMenuItem("Quit", "quit noXa")

	// recover is per-goroutine: the menu event loop needs its own guard (331).
	go guardCrash("tray", func() {
		for {
			select {
			case <-t.miShowHide.ClickedCh:
				t.toggleWindow()
			case <-t.miMute.ClickedCh:
				t.app.emitHotkey("mute_toggle")
			case <-t.miDeafen.ClickedCh:
				t.app.emitHotkey("deafen_toggle")
			case <-t.miReconnect.ClickedCh:
				t.reconnectLast()
			case <-t.miDisconnect.ClickedCh:
				t.disconnectActive()
			case <-miQuit.ClickedCh:
				t.app.Quit()
				return
			}
		}
	})
}

// setConnected synchronizes reconnect availability with the active tab. The
// mutex serializes state changes and MenuItem updates from connection relays.
func (t *tray) setConnected(isConnected bool) {
	t.mu.Lock()
	// The voice flags stay as the frontend last reported them: trayStateFor
	// masks them while disconnected, and the frontend reports its own voice
	// teardown, so a state that arrived before the connection is kept.
	t.status.connected = isConnected
	if t.miReconnect != nil {
		if isConnected {
			t.miReconnect.Disable()
		} else {
			t.miReconnect.Enable()
		}
	}
	if t.miDisconnect != nil {
		if isConnected {
			t.miDisconnect.Enable()
		} else {
			t.miDisconnect.Disable()
		}
	}
	t.updateTitleLocked()
	t.mu.Unlock()
}

// traySetConnected reflects the active tab's connection state in the tray.
func traySetConnected(isConnected bool) {
	if trayCtl == nil {
		return
	}
	trayCtl.setConnected(isConnected)
}

// reconnectLast asks the frontend to reuse its credential-bearing last
// connection record. The connected check is repeated in the frontend as a
// defense against a delayed native-menu click.
func (t *tray) reconnectLast() {
	t.mu.Lock()
	isConnected, emit := t.status.connected, t.emit
	t.mu.Unlock()
	if isConnected || emit == nil {
		return
	}
	emit("tray_reconnect", nil)
}

// disconnectActive routes through the frontend so it cancels any pending
// retry and clears the automatic-reconnect target before closing the tab.
func (t *tray) disconnectActive() {
	t.mu.Lock()
	isConnected, emit := t.status.connected, t.emit
	t.mu.Unlock()
	if !isConnected || emit == nil {
		return
	}
	emit("tray_disconnect", nil)
}

// toggleWindow shows or hides the main window (287).
func (t *tray) toggleWindow() {
	t.mu.Lock()
	t.visible = !t.visible
	visible := t.visible
	t.mu.Unlock()
	if t.app.ctx == nil {
		return
	}
	if visible {
		wailsRuntime.WindowShow(t.app.ctx)
		t.miShowHide.SetTitle("Hide noXa")
	} else {
		wailsRuntime.WindowHide(t.app.ctx)
		t.miShowHide.SetTitle("Show noXa")
	}
}

// trayMarkHidden records that the window was hidden by something other than
// the tray menu (288 minimize-to-tray), so the next menu click shows it again
// instead of hiding an already-hidden window.
func trayMarkHidden() {
	if trayCtl == nil {
		return
	}
	trayCtl.mu.Lock()
	trayCtl.visible = false
	trayCtl.mu.Unlock()
	if trayCtl.miShowHide != nil {
		trayCtl.miShowHide.SetTitle("Show noXa")
	}
}

// updateTitle serializes native updates so an older state cannot overwrite a
// newer transition. Identical presentations never touch the operating system.
func (t *tray) updateTitle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.updateTitleLocked()
}

// The tray starts before the main window exists. Reapply its latest state once
// Wails has created the window, even if no voice state has changed since then.
func trayRefreshWindowIcon() {
	if trayCtl == nil {
		return
	}
	trayCtl.mu.Lock()
	defer trayCtl.mu.Unlock()
	if err := setTaskbarIcon(trayCtl.last.icon); err != nil {
		log.Printf("taskbar icon: %v", err)
	}
}

func (t *tray) updateTitleLocked() {
	if t.publish == nil {
		return
	}
	p := trayPresent(t.status)
	if t.presented && p == t.last {
		return
	}
	t.publish(p, !t.presented || p.icon != t.last.icon)
	t.last, t.presented = p, true
}

func (t *tray) setVoiceState(speaking, muted, deafened, inChannel bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Store the raw flags: the frontend deduplicates its reports, so a state
	// masked here while not yet connected would never be sent again.
	s := &t.status
	if t.presented && s.speaking == speaking && s.muted == muted && s.deafened == deafened && s.inChannel == inChannel {
		return
	}
	s.speaking, s.muted, s.deafened, s.inChannel = speaking, muted, deafened, inChannel
	t.updateTitleLocked()
}

func (t *tray) setUnread(count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	count = max(count, 0)
	if t.presented && t.status.unread == count {
		return
	}
	t.status.unread = count
	t.updateTitleLocked()
}

// SetTrayVoiceState receives the active voice session's detected speech,
// independent input/output mute flags, and whether a voice session is live in
// a channel, including while the window is hidden.
func (a *App) SetTrayVoiceState(speaking, muted, deafened, inChannel bool) {
	if trayCtl != nil {
		trayCtl.setVoiceState(speaking, muted, deafened, inChannel)
	}
}

// SetTrayUnread receives the frontend's count of unseen messages (channels,
// DMs, private groups, background server tabs). The tray shows 1-9 or "9+";
// the tooltip carries the exact number. Zero hides the counter.
func (a *App) SetTrayUnread(count int) {
	if trayCtl != nil {
		trayCtl.setUnread(count)
	}
}

// trayAddMention bumps the mention badge (290) and refreshes the tooltip.
func trayAddMention() {
	if trayCtl == nil {
		return
	}
	trayCtl.mu.Lock()
	trayCtl.status.mentions++
	trayCtl.mu.Unlock()
	trayCtl.updateTitle()
}

// trayClearMentions resets the badge (window focused / tab switched).
func trayClearMentions() {
	if trayCtl == nil {
		return
	}
	trayCtl.mu.Lock()
	trayCtl.status.mentions = 0
	trayCtl.mu.Unlock()
	trayCtl.updateTitle()
}

// TrayMention bumps the tray badge for a mention that arrived in the ACTIVE
// tab (290). Background tabs are counted by the event relay, which never sees
// the active tab's frames, so the frontend has to report those itself.
func (a *App) TrayMention() {
	trayAddMention()
}

// TrayClearMentions resets the tray badge (290), e.g. once the window is
// focused again and the user has necessarily seen the messages.
func (a *App) TrayClearMentions() {
	trayClearMentions()
}
