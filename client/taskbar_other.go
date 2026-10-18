//go:build !windows

package main

func setTaskbarIcon(trayIconKey) error { return nil }

func shutdownTaskbarOverlay() error { return nil }
