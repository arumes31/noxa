//go:build !windows

package main

import "fmt"

func nativeGamingOverlayAvailable() bool                  { return false }
func nativeGamingOverlayMonitors() []GamingOverlayMonitor { return nil }
func newNativeGamingOverlay() (gamingOverlayWindow, error) {
	return nil, fmt.Errorf("gaming overlay is available on Windows only")
}
