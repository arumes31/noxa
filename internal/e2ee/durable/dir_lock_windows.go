//go:build windows

package durable

import "time"

// lockStoreDir is a no-op on Windows: an open store handle already makes a
// rename of its directory fail, which is the atomic check there.
func lockStoreDir(string, bool, time.Duration) (func(), error) { return func() {}, nil }
