package main

import (
	"runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/options"
)

// Windows integration (88, 90): start with Windows, the noxa:// protocol and
// the single-instance lock. Registry access lives in desktop_windows.go; the
// decisions and value formats stay here so they are tested on every platform.
const (
	autostartRunKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName  = "noXa"
	minimizedArgument   = "--minimized"
	deepLinkScheme      = "noxa://"
	deepLinkMaxLength   = 512
	deepLinkEvent       = "deep_link"
	singleInstanceID    = "noxa-desktop-8f3c2a61"
	protocolKey         = `Software\Classes\noxa`
	protocolCommandKey  = protocolKey + `\shell\open\command`
	protocolDescription = "URL:noXa Protocol"
)

// Launch state, captured once at the top of runApp before any update code
// clears its environment variables.
var (
	launchHidden          bool
	launchDeepLink        string
	singleInstanceEnabled bool
)

func captureLaunch(args []string, getenv func(string) string) {
	minimized, link := readLaunchArgs(args)
	// A clicked link must open a visible confirmation, so it wins over --minimized.
	launchHidden = minimized && link == ""
	launchDeepLink = link
	singleInstanceEnabled = singleInstanceAllowed(startedAfterUpdate, incomingHandover != nil, getenv)
}

// readLaunchArgs reports --minimized and the first acceptable noxa:// URL.
func readLaunchArgs(args []string) (minimized bool, link string) {
	for _, arg := range args {
		minimized = minimized || arg == minimizedArgument
		if link == "" {
			link = deepLinkArg(arg)
		}
	}
	return minimized, link
}

// deepLinkArg accepts only a bounded noxa:// URL of printable ASCII without
// quotes or backslashes. The frontend still parses it strictly as an invite.
func deepLinkArg(arg string) string {
	if len(arg) <= len(deepLinkScheme) || len(arg) > deepLinkMaxLength || !strings.EqualFold(arg[:len(deepLinkScheme)], deepLinkScheme) {
		return ""
	}
	for i := 0; i < len(arg); i++ {
		if c := arg[i]; c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return ""
		}
	}
	return arg
}

// singleInstanceAllowed exempts every process the updater started: the
// handover child must run beside the old process, and a forwarded launch would
// exit and silently fail the update.
func singleInstanceAllowed(afterUpdate, handover bool, getenv func(string) string) bool {
	if afterUpdate || handover {
		return false
	}
	for _, name := range []string{updateHandoverEnv, updateReadyAddressEnv, updateReadyNonceEnv, updateReconnectEnv, updateWindowEnv} {
		if getenv(name) != "" {
			return false
		}
	}
	return true
}

func autostartCommand(exe string) string { return `"` + exe + `" ` + minimizedArgument }

func protocolCommand(exe string) string { return `"` + exe + `" "%1"` }

// deepLinkInbox holds a link until the frontend has initialized.
type deepLinkInbox struct {
	mu      sync.Mutex
	ready   bool
	pending string
}

func (a *App) deliverDeepLink(link string) {
	if link == "" {
		return
	}
	a.deepLink.mu.Lock()
	ready := a.deepLink.ready
	if !ready {
		// shortcut: only the newest link before startup is kept; queue more if multi-link launches matter.
		a.deepLink.pending = link
	}
	a.deepLink.mu.Unlock()
	if ready {
		a.emitPlain(deepLinkEvent, link)
	}
}

// TakeDeepLink is called once the frontend has initialized: later links
// arrive as "deep_link" events, an earlier one is returned here.
func (a *App) TakeDeepLink() string {
	a.deepLink.mu.Lock()
	defer a.deepLink.mu.Unlock()
	a.deepLink.ready = true
	link := a.deepLink.pending
	a.deepLink.pending = ""
	return link
}

// secondInstanceLaunch raises the running window and forwards a noxa:// URL
// from the second launch; every other argument is ignored.
func (a *App) secondInstanceLaunch(data options.SecondInstanceData) {
	if a.ctx != nil { // no window yet: nothing to raise, the link still queues
		nativeToastRaise(a) // the same show/unminimize/tray bookkeeping as a toast click
	}
	_, link := readLaunchArgs(data.Args)
	a.deliverDeepLink(link)
}

// desktopSingleInstanceLock is nil for update-launched processes (see
// singleInstanceAllowed) and outside Windows, where noxa:// is not registered.
func desktopSingleInstanceLock(app *App) *options.SingleInstanceLock {
	if !singleInstanceEnabled || runtime.GOOS != "windows" {
		return nil
	}
	return &options.SingleInstanceLock{UniqueId: singleInstanceID, OnSecondInstanceLaunch: app.secondInstanceLaunch}
}

// GetStartWithWindows reports whether noXa starts (minimized) at Windows sign-in.
func (a *App) GetStartWithWindows() bool { return startWithWindowsEnabled() }

// SetStartWithWindows adds or removes the per-user Run entry.
func (a *App) SetStartWithWindows(enabled bool) error { return setStartWithWindows(enabled) }
