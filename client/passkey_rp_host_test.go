package main

import "testing"

// Auto-endorsement only runs for a named host the desktop dialed (audit #6).
func TestPasskeyRPHost(t *testing.T) {
	for addr, want := range map[string]string{
		"noxa.example.com:12333": "noxa.example.com",
		"NOXA.Example.COM.:443":  "noxa.example.com",
		"noxa.example.com":       "noxa.example.com",
		"127.0.0.1:12333":        "",
		"[2001:db8::1]:12333":    "",
		"":                       "",
	} {
		if got := passkeyRPHost(addr); got != want {
			t.Errorf("passkeyRPHost(%q) = %q, want %q", addr, got, want)
		}
	}
}
