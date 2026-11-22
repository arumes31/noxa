package devicetrust

import (
	"bytes"
	"crypto/ed25519"

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
// are left out. The first device can only be revoked by itself, under its own
// device ID: no other device of the account may take the root's place in the
// chain, and an entry that carries the root's key under another ID is refused.
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
	var rootKey ed25519.PublicKey
	for _, d := range trusted {
		byID[d.ID] = d
		if d.First {
			root, rootKey = d.ID, d.Signing
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
		// A revoked device's key must be a valid signing key so the pin can
		// match it later. The root can only revoke itself: an entry naming
		// its ID or carrying its key counts only as its own self-revocation.
		key, err := LoadPublicKey(r.Ed25519Public)
		if err != nil {
			continue
		}
		if (r.DeviceID == root || bytes.Equal(key, rootKey)) && (r.DeviceID != root || r.SignerDeviceID != root) {
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
