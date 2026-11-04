//go:build windows

package webview2

// boolToInt encodes a Go bool as the scalar Win32 BOOL (0 or 1) that WebView2
// property setters take by value. Passing the address of a Go bool instead is
// always nonzero and therefore always means TRUE to the native side.
func boolToInt(input bool) int {
	if input {
		return 1
	}
	return 0
}
