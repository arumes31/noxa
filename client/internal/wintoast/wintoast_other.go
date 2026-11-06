//go:build !windows

package wintoast

// SetCallback is a no-op off Windows.
func SetCallback(Callback) {}

// Register reports ErrUnsupported off Windows.
func Register(AppData) error { return ErrUnsupported }

// Show reports ErrUnsupported off Windows.
func Show(string, Toast) error { return ErrUnsupported }

// Remove reports ErrUnsupported off Windows.
func Remove(string, string, string) error { return ErrUnsupported }

// Clear reports ErrUnsupported off Windows.
func Clear(string) error { return ErrUnsupported }
