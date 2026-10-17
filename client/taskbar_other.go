//go:build !windows

package main

func setTaskbarIcon(trayIconState) error { return nil }

func shutdownTaskbarOverlay() error { return nil }
