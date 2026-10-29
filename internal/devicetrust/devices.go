// Package devicetrust validates a current directory against independently pinned
// noXa signing identity and its authenticated keyrecord encryption key.
package devicetrust

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"noxa/internal/auth"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
)

var ErrUntrusted = errors.New("untrusted device directory")

type Device struct {
	ID         string
	Kind       string
	Signing    ed25519.PublicKey
	Encryption [32]byte
	First      bool
}

func Verify(principal string, signing ed25519.PublicKey, encryption [32]byte, list []netproto.DMDevice) ([]Device, error) {
	if !olmdir.ValidText(principal) || len(list) > 64 {
		return nil, ErrUntrusted
	}
	if len(list) == 0 {
		return nil, nil
	}
	byID := map[string]netproto.DMDevice{}
	keys := map[string]Device{}
	root := ""
	for _, d := range list {
		if !olmdir.ValidText(d.DeviceID) {
			return nil, ErrUntrusted
		}
		if _, ok := byID[d.DeviceID]; ok {
			return nil, ErrUntrusted
		}
		byID[d.DeviceID] = d
		if d.FirstDevice {
			if root != "" {
				return nil, ErrUntrusted
			}
			root = d.DeviceID
		}
		if len(d.Ed25519Public) > 256 || len(d.X25519Public) != 44 {
			continue
		}
		k, e := auth.LoadPublicKey(d.Ed25519Public)
		x, e2 := base64.StdEncoding.Strict().DecodeString(d.X25519Public)
		if d.RevokedAt != 0 || e != nil || e2 != nil || len(x) != 32 || base64.StdEncoding.EncodeToString(x) != d.X25519Public || !olmdir.ValidSigning(base64.RawStdEncoding.EncodeToString(k)) || !olmdir.ValidCurve(base64.RawStdEncoding.EncodeToString(x)) {
			continue
		}
		var enc [32]byte
		copy(enc[:], x)
		keys[d.DeviceID] = Device{d.DeviceID, d.Kind, k, enc, d.FirstDevice}
	}
	first, ok := keys[root]
	if !ok || len(signing) != 32 || !bytes.Equal(first.Signing, signing) || first.Encryption != encryption {
		return nil, ErrUntrusted
	}
	trusted := map[string]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, d := range list {
			if trusted[d.DeviceID] || !trusted[d.ApprovedBy] {
				continue
			}
			if _, ok := keys[d.DeviceID]; !ok {
				continue
			}
			if len(d.ApprovalSignature) == 64 && ed25519.Verify(keys[d.ApprovedBy].Signing, netproto.DeviceApprovalTranscript(principal, d.ApprovedBy, d.Ed25519Public, d.X25519Public), d.ApprovalSignature) {
				trusted[d.DeviceID] = true
				changed = true
			}
		}
	}
	out := []Device{first}
	for _, d := range list {
		if d.DeviceID != root && trusted[d.DeviceID] {
			out = append(out, keys[d.DeviceID])
		}
	}
	return out, nil
}
