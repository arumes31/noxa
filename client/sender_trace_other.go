//go:build !windows

package main

import "errors"

func supportsNativeSenderTrace() bool { return false }
func armNativeSenderTrace(string) (string, error) {
	return "", errors.New("native sender timing is unavailable on this platform")
}
