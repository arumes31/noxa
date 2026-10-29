//go:build windows

package durable

// LockForDestroy on Windows probes (ProbeUnlocked) and holds nothing: every
// store opener keeps an open handle, so renaming the store directory itself
// fails while any process has the store open. The rename is the atomic check
// (holding handles here would make the rename fail for us too).
func LockForDestroy(dir string) (release func(), err error) {
	if err := ProbeUnlocked(dir); err != nil {
		return nil, err
	}
	return func() {}, nil
}
