//go:build !windows

package main

func nativeGamingOverlayAvailable() bool                  { return false }
func nativeGamingOverlayMonitors() []GamingOverlayMonitor { return nil }
func (a *App) createGamingOverlayLocked() string {
	return "gaming overlay is available on Windows only"
}
