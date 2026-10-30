//go:build !windows

package main

func platformCaptureUpdateWindow() (*updateWindowPlacement, error) { return nil, nil }
func platformApplyUpdateWindow(updateWindowPlacement) error        { return nil }
