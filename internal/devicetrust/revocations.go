package devicetrust

import (
	"crypto/ed25519"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// VerifiedRevocation is a revocation whose signature chains to the account's
// pinned identity: a device of the trusted set signed it.
type VerifiedRevocation struct {
	DeviceID      string
	Ed25519Public string
	SignerID      string
	SignedAt      int64
}

// VerifyRevocations returns the signed revocations of principal that a
// device in trusted (the result of Verify for the same account) signed over
// DeviceRevocationTranscript. Unsigned (server-only) entries, malformed
// entries, forged signatures, signers outside the trusted set and duplicates
// are left out. The first device can only be revoked by itself: no other
// device of the account may take the root's place in the chain.
//
// A revocation stays valid after its signer is revoked in turn, but a client
// can only verify it while the signer is still trusted; clients pin verified
// revocations (by the revoked signing key), so a later directory that lists
// the device as active again cannot bring it back.
func VerifyRevocations(principal string, trusted []Device, list []netproto.DeviceRevocation) []VerifiedRevocation {
	if len(list) > netproto.MaxListedRevocations {
		list = list[:netproto.MaxListedRevocations]
	}
	byID := make(map[string]Device, len(trusted))
	root := ""
	for _, d := range trusted {
		byID[d.ID] = d
		if d.First {
			root = d.ID
		}
	}
	seen := map[string]bool{}
	var out []VerifiedRevocation
	for _, r := range list {
		if !r.Signed() || !netproto.ValidRevocationFields(r) || seen[r.DeviceID] {
			continue
		}
		signer, ok := byID[r.SignerDeviceID]
		if !ok || len(signer.Signing) != ed25519.PublicKeySize {
			continue
		}
		// The root can only revoke itself; a revoked device's key must be a
		// valid signing key so the pin can match it later.
		if r.DeviceID == root && r.SignerDeviceID != root {
			continue
		}
		if _, err := auth.LoadPublicKey(r.Ed25519Public); err != nil {
			continue
		}
		transcript := netproto.DeviceRevocationTranscript(principal, r.SignerDeviceID, r.DeviceID, r.Ed25519Public, r.X25519Public, r.SignedAt, r.Reason)
		if !ed25519.Verify(signer.Signing, transcript, r.Signature) {
			continue
		}
		seen[r.DeviceID] = true
		out = append(out, VerifiedRevocation{DeviceID: r.DeviceID, Ed25519Public: r.Ed25519Public, SignerID: r.SignerDeviceID, SignedAt: r.SignedAt})
	}
	return out
}
