package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// deviceRevocationStore is the optional part of DeviceStore for signed
// revocations (38). *store.Store implements it; a store without it keeps
// unsigned, server-only revocations.
type deviceRevocationStore interface {
	RevokeDeviceSigned(ctx context.Context, principal, deviceID string, sig store.SignedRevocation) (store.Device, error)
	ListDeviceRevocations(ctx context.Context, principal string, limit int) ([]store.DeviceRevocation, error)
}

var errInvalidRevocationSignature = errors.New("invalid device revocation signature")

// verifyRevocationSignature checks a signed revoke request: the signing
// device is the requesting session's own, still active and trusted (first
// device or approved), the time is close to the server's clock, and the
// signature covers the target's exact keys. now is the server time.
func verifyRevocationSignature(principal string, signer, target store.Device, signedAt int64, signature []byte, now time.Time) error {
	if signer.DeviceID == "" || !signer.Active() || (!signer.FirstDevice && len(signer.ApprovalSignature) == 0) {
		return errInvalidRevocationSignature
	}
	if target.Ed25519Public == "" || len(signature) != ed25519.SignatureSize {
		return errInvalidRevocationSignature
	}
	at := time.Unix(signedAt, 0)
	if signedAt <= 0 || at.Before(now.Add(-netproto.MaxDeviceRevocationSkew)) || at.After(now.Add(netproto.MaxDeviceRevocationSkew)) {
		return errInvalidRevocationSignature
	}
	key, err := auth.LoadPublicKey(signer.Ed25519Public)
	if err != nil {
		return errInvalidRevocationSignature
	}
	rev := netproto.DeviceRevocation{DeviceID: target.DeviceID, Ed25519Public: target.Ed25519Public, X25519Public: target.X25519Public,
		Reason: netproto.DeviceRevocationReasonUser, SignerDeviceID: signer.DeviceID, SignedAt: signedAt, Signature: signature}
	if !netproto.ValidRevocationFields(rev) {
		return errInvalidRevocationSignature
	}
	transcript := netproto.DeviceRevocationTranscript(principal, signer.DeviceID, target.DeviceID, target.Ed25519Public, target.X25519Public, signedAt, netproto.DeviceRevocationReasonUser)
	if !ed25519.Verify(key, transcript, signature) {
		return errInvalidRevocationSignature
	}
	return nil
}

// revokeDeviceSigned revokes like revokeDevice and stores the signature.
func (s *TCPServer) revokeDeviceSigned(ctx context.Context, principal, deviceID string, sig store.SignedRevocation, actor string) (store.Device, error) {
	revocations, ok := s.deps.Devices.(deviceRevocationStore)
	if !ok {
		return s.revokeDevice(ctx, principal, deviceID, store.DeviceRevokedByUser, actor)
	}
	d, err := revocations.RevokeDeviceSigned(ctx, principal, deviceID, sig)
	if err != nil {
		return store.Device{}, err
	}
	s.afterDevicesRevoked(ctx, principal, []store.Device{d}, store.DeviceRevokedByUser, actor)
	return d, nil
}

// dmRevocations lists the principal's recent revocations for the key
// directory, signed ones with their statement.
func (s *TCPServer) dmRevocations(ctx context.Context, principal string) ([]netproto.DeviceRevocation, error) {
	if s.deps == nil || s.deps.Devices == nil || principal == "" {
		return nil, nil
	}
	revocations, ok := s.deps.Devices.(deviceRevocationStore)
	if !ok {
		return nil, nil
	}
	list, err := revocations.ListDeviceRevocations(ctx, principal, netproto.MaxListedRevocations)
	if err != nil {
		return nil, err
	}
	out := make([]netproto.DeviceRevocation, 0, len(list))
	for _, r := range list {
		entry := netproto.DeviceRevocation{DeviceID: r.DeviceID, Ed25519Public: r.Ed25519Public, X25519Public: r.X25519Public,
			RevokedAt: r.RevokedAt.Unix(), Reason: r.Reason}
		if r.SignerDeviceID != "" && len(r.Signature) == ed25519.SignatureSize {
			entry.SignerDeviceID, entry.SignedAt, entry.Signature = r.SignerDeviceID, r.SignedAt, r.Signature
		}
		if netproto.ValidRevocationFields(entry) {
			out = append(out, entry)
		}
	}
	return out, nil
}
