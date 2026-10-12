//go:build !windows

package main

import "errors"

type unsupportedDisplayCapturePower struct{}

func newDisplayCapturePowerBackend() displayCapturePowerBackend {
	return unsupportedDisplayCapturePower{}
}

func (unsupportedDisplayCapturePower) create() (uintptr, error) {
	return 0, errors.New("native display capture keep-awake is unsupported on this platform")
}
func (unsupportedDisplayCapturePower) set(uintptr, uint32) error   { return nil }
func (unsupportedDisplayCapturePower) clear(uintptr, uint32) error { return nil }
func (unsupportedDisplayCapturePower) close(uintptr) error         { return nil }

func (unsupportedDisplayCapturePower) state() (uint64, bool, error) {
	return 0, false, errors.New("native display capture keep-awake is unsupported on this platform")
}
func (unsupportedDisplayCapturePower) shutdown() error { return nil }
