package main

import (
	"crypto/ed25519"
	"errors"
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

// olmTrustExpectation holds the independently known values a signed Olm bundle
// must match. SigningKey is the peer device's noXa Ed25519 key from the pinned
// root/approval chain (dmDeviceList), never from Olm or admission data.
// Identity is the locally pinned Olm identity; only on first adoption is it the
// admission target's identity, which the signed bundle must then match exactly.
type olmTrustExpectation struct {
	SigningKey   ed25519.PublicKey
	Server       string
	Principal    string
	Device       string
	AccountEpoch int64
	Identity     string
}

var errOlmEvidence = errors.New("invalid Olm bundle evidence")

// verifyOlmBundleEvidence authenticates retained signed account evidence (an
// admitted delivery's source bundle) without publication TTL. Native mirror of
// the browser verifyBundleEvidence.
func verifyOlmBundleEvidence(b olmdir.Bundle, x olmTrustExpectation) error {
	if len(x.SigningKey) != ed25519.PublicKeySize || x.Identity == "" || b.AccountEpoch != x.AccountEpoch || b.Identity != x.Identity {
		return errOlmEvidence
	}
	if b.VerifyEvidence(x.SigningKey, x.Server, x.Principal, x.Device) != nil {
		return errOlmEvidence
	}
	return nil
}

// verifyReservedClaim checks a fresh directory claim response for a request
// reserved locally with claimUntil. Native mirror of the browser
// verifyReservedClaim: epoch and identity must equal the independent
// expectation, the bundle must be signed by the pinned noXa device key, and
// the claimed one-time key must be listed in it.
func verifyReservedClaim(b *olmdir.Bundle, x olmTrustExpectation, keyID string, claimUntil int64, now time.Time) (olmdir.Key, error) {
	if b == nil || len(x.SigningKey) != ed25519.PublicKeySize || x.Identity == "" || b.AccountEpoch != x.AccountEpoch || b.Identity != x.Identity {
		return olmdir.Key{}, errOlmEvidence
	}
	key, e := b.VerifyReservedClaim(x.SigningKey, x.Server, x.Principal, x.Device, x.Identity, keyID, claimUntil, now)
	if e != nil {
		return olmdir.Key{}, errOlmEvidence
	}
	return key, nil
}
