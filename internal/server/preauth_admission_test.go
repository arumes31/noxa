package server

import "testing"

// A handful of sources must never be able to fill the global pre-auth cap
// and refuse every real login (audit #9).
func TestPreauthCapNeedsManySources(t *testing.T) {
	if sources := maxPreauthConnections / maxPreauthPerIP; sources < 100 {
		t.Fatalf("%d sources fill the pre-auth cap (%d global, %d per source); want at least 100", sources, maxPreauthConnections, maxPreauthPerIP)
	}
}
