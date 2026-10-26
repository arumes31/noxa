package main

import (
	"crypto/ed25519"
	"noxa/internal/devicetrust"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"time"
)

// verifyOlmClaim is the future ratchet consumer boundary. The expected server,
// principal/device, existing root pin and authenticated encryption record must
// come from local trust state, never be copied from the claimed bundle.
func verifyOlmClaim(b olmdir.Bundle, server, principal, device, pinnedOlmIdentity, keyID string, root ed25519.PublicKey, rootX [32]byte, current []netproto.DMDevice, now time.Time) (olmdir.Key, error) {
	devices, e := devicetrust.Verify(principal, root, rootX, current)
	if e != nil {
		return olmdir.Key{}, e
	}
	for _, d := range devices {
		if d.ID == device {
			return b.VerifyClaim(d.Signing, server, principal, device, pinnedOlmIdentity, keyID, now)
		}
	}
	return olmdir.Key{}, devicetrust.ErrUntrusted
}
