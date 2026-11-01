//go:build !windows && !linux && !darwin

package main

import "errors"

// Never fall back to a replace-capable rename or unlink a running executable
// on a platform with no validated exclusive-rename primitive.
func renameClientUpdateRecoveryNoReplace(source, destination string) error {
	return errors.New("exclusive recovery rename is unavailable on this platform")
}
