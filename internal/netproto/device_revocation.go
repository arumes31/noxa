package netproto

import (
	"strconv"
	"strings"
	"time"
)

// Signed device revocations (38).
//
// A revocation the account's own user makes is signed by the device that
// made it, with the same Ed25519 device key that signs approvals
// (DeviceApprovalTranscript). The server stores the statement and lists it
// next to the account's active devices (KeyResponse.Revocations), so contacts
// and the account's other devices can check that a trusted device of the
// account (not the server alone) revoked it, and keep refusing that device
// even if a directory later lists it as active again.
//
// Revocations without a signature (an administrator, idle expiry, an
// integration, or an older client) are still listed, with SignerDeviceID and
// Signature empty, so clients can show that the server revoked the device;
// they are honoured as the server's decision but never pinned as permanent.

// DeviceRevocationReasonUser is the only reason a device signs today: the
// account's user signed the device out.
const DeviceRevocationReasonUser = "user"

// MaxDeviceRevocationSkew bounds how far a signed revocation time may differ
// from the server's clock when the server accepts it.
const MaxDeviceRevocationSkew = 10 * time.Minute

// MaxListedRevocations bounds the revocations one KeyResponse carries.
const MaxListedRevocations = 32

// DeviceRevocation is one revoked device of an account as the key directory
// lists it. RevokedAt is the server's revocation time (unix seconds); for a
// signed revocation SignedAt is the time the signing device put in the
// statement.
type DeviceRevocation struct {
	DeviceID       string `json:"device_id"`
	Ed25519Public  string `json:"ed25519_public_key"`
	X25519Public   string `json:"x25519_public_key"`
	RevokedAt      int64  `json:"revoked_at"`
	Reason         string `json:"reason,omitempty"`
	SignerDeviceID string `json:"signer_device_id,omitempty"`
	SignedAt       int64  `json:"signed_at,omitempty"`
	Signature      []byte `json:"signature,omitempty"`
}

// Signed reports whether the revocation carries a device signature.
func (r DeviceRevocation) Signed() bool {
	return r.SignerDeviceID != "" && len(r.Signature) > 0
}

// DeviceRevocationTranscript is the message the revoking device signs. It
// binds the account, the signing device, the revoked device's exact keys, the
// signing time and the reason, one field per line. Only the Ed25519 key, a
// PEM block as in DeviceApprovalTranscript, spans several lines; every other
// field is single-line (ValidRevocationFields).
func DeviceRevocationTranscript(principal, signerDeviceID, deviceID, ed25519Public, x25519Public string, signedAt int64, reason string) []byte {
	return []byte("noxa-device-revocation-v1\n" + principal + "\n" + signerDeviceID + "\n" + deviceID + "\n" +
		ed25519Public + "\n" + x25519Public + "\n" + strconv.FormatInt(signedAt, 10) + "\n" + reason)
}

// ValidRevocationFields checks the shape of a listed revocation: bounded,
// single-line fields, and a complete signature block or none at all.
func ValidRevocationFields(r DeviceRevocation) bool {
	single := func(s string, maxLen int) bool {
		return len(s) <= maxLen && !strings.ContainsAny(s, "\r\n\x00")
	}
	// The Ed25519 key is a PEM block (several lines, as in
	// DeviceApprovalTranscript); verifiers parse it with auth.LoadPublicKey.
	pem := len(r.Ed25519Public) <= 256 && !strings.ContainsAny(r.Ed25519Public, "\r\x00")
	if r.DeviceID == "" || !single(r.DeviceID, 64) || !pem || !single(r.X25519Public, 64) ||
		!single(r.Reason, 32) || !single(r.SignerDeviceID, 64) || r.RevokedAt < 0 {
		return false
	}
	if r.SignerDeviceID == "" && len(r.Signature) == 0 {
		return r.SignedAt == 0
	}
	return r.SignerDeviceID != "" && len(r.Signature) == 64 && r.SignedAt > 0 && r.Reason == DeviceRevocationReasonUser
}
