package server

import (
	"os"
	"testing"

	"noxa/internal/netproto"
)

// TestMain overwrites every pooled control frame buffer once it is released
// (#3), so a handler that kept payload bytes past dispatch reads garbage and
// fails its test instead of silently racing in production.
func TestMain(m *testing.M) {
	netproto.SetFramePoisoning(true)
	os.Exit(m.Run())
}
