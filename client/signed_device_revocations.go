// signed_device_revocations.go pins device revocations that a trusted device
// of an account signed (38). The key directory lists them next to the
// account's devices; once one verifies against the account's device chain it
// is recorded permanently (per local identity, server and account, next to
// the contact trust pins), the device is dropped from every later device list
// of that account, whatever the directory serves, and the secure-messaging
// store gets a revoked trust record, so the device's sessions end and its
// future messages are refused.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"sync"

	"noxa/internal/auth"
	"noxa/internal/devicetrust"
	"noxa/internal/netproto"
)

var signedRevocationsMu sync.Mutex

// signedRevocationPin is one verified revocation.
type signedRevocationPin struct {
	DeviceID      string `json:"device_id"`
	Ed25519Public string `json:"ed25519_public_key"`
	SignerID      string `json:"signer_device_id"`
	SignedAt      int64  `json:"signed_at"`
}

type signedRevocationRecord struct {
	Version int                   `json:"version"`
	Pins    []signedRevocationPin `json:"pins"`
}

// DeviceRevokedEvent tells the frontend that a device of an account was
// revoked, for a notice in that account's conversations. Signed is true when
// a device of the account signed it (and it is pinned); false when only the
// server's directory reports it.
type DeviceRevokedEvent struct {
	TabID    string `json:"tab_id,omitempty"`
	UniqueID string `json:"unique_id"`
	DeviceID string `json:"device_id"`
	Signed   bool   `json:"signed"`
	SignerID string `json:"signer_device_id,omitempty"`
}

// maxSignedRevocationPins bounds one account's record.
const maxSignedRevocationPins = 256

// signedRevocationsPath names the record for this identity, server and uid.
func (m *connManager) signedRevocationsPath(uid string) (string, error) {
	id, err := m.identity()
	if err != nil {
		return "", err
	}
	local, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	addr := m.addr
	m.mu.Unlock()
	server, err := canonicalServerDestination(addr)
	if err != nil || server == "" || uid == "" {
		return "", errors.New("server identity unavailable")
	}
	if identityDeleted(id.PublicKey) {
		return "", errIdentityDeleted
	}
	return dmPinPathFor(local, server, "\x00signed-device-revocations\x00"+uid)
}

func readSignedRevocations(path string) (signedRevocationRecord, error) {
	var r signedRevocationRecord
	raw, err := os.ReadFile(path) // #nosec G304 -- application-owned path derived from hashed identity data.
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, errors.New("corrupt signed device revocation record")
	}
	return r, nil
}

// pinSignedRevocations adds verified to uid's record and returns every pin
// and the newly added ones. An unreadable record is an error: callers must
// not treat it as "nothing revoked".
func (m *connManager) pinSignedRevocations(uid string, verified []devicetrust.VerifiedRevocation) (all, added []signedRevocationPin, err error) {
	path, err := m.signedRevocationsPath(uid)
	if err != nil {
		return nil, nil, nil // no namespace yet: nothing can have been pinned
	}
	signedRevocationsMu.Lock()
	defer signedRevocationsMu.Unlock()
	r, err := readSignedRevocations(path)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, p := range r.Pins {
		known[p.DeviceID] = true
	}
	for _, v := range verified {
		if known[v.DeviceID] || len(r.Pins) >= maxSignedRevocationPins {
			continue
		}
		pin := signedRevocationPin{DeviceID: v.DeviceID, Ed25519Public: v.Ed25519Public, SignerID: v.SignerID, SignedAt: v.SignedAt}
		r.Pins = append(r.Pins, pin)
		added = append(added, pin)
		known[v.DeviceID] = true
	}
	if len(added) > 0 {
		r.Version = 1
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, nil, err
		}
		if err := writePrivateFileAtomic(path, raw); err != nil {
			return nil, nil, err
		}
	}
	return r.Pins, added, nil
}

// withoutPinnedRevocations drops devices whose ID or signing key a pin names.
func withoutPinnedRevocations(devices []dmTrustedDevice, pins []signedRevocationPin) []dmTrustedDevice {
	if len(pins) == 0 {
		return devices
	}
	ids := map[string]bool{}
	var keys []ed25519.PublicKey
	for _, p := range pins {
		ids[p.DeviceID] = true
		if k, err := auth.LoadPublicKey(p.Ed25519Public); err == nil {
			keys = append(keys, k)
		}
	}
	out := devices[:0:0]
	for _, d := range devices {
		revoked := ids[d.DeviceID]
		for _, k := range keys {
			if bytes.Equal(k, d.Signing) {
				revoked = true
			}
		}
		if !revoked {
			out = append(out, d)
		}
	}
	return out
}

// revokedDevice reports whether a pin or a local revocation names a device by
// ID or signing key. The first device is the account identity itself and is
// never treated as revoked here: its approvals anchor every other device.
func revokedDevice(d netproto.DMDevice, pins []signedRevocationPin, ownRevoked []string) bool {
	if d.FirstDevice {
		return false
	}
	if slices.Contains(ownRevoked, d.DeviceID) {
		return true
	}
	key, keyErr := auth.LoadPublicKey(d.Ed25519Public)
	for _, p := range pins {
		if p.DeviceID == d.DeviceID {
			return true
		}
		if pinned, err := auth.LoadPublicKey(p.Ed25519Public); keyErr == nil && err == nil && bytes.Equal(pinned, key) {
			return true
		}
	}
	return false
}

// ownRevocationState returns uid's pinned signed revocations and, for this
// account, the devices revoked from this desktop. Unreadable records are
// errors: callers must not treat them as "nothing revoked".
func (m *connManager) ownRevocationState(uid string) ([]signedRevocationPin, []string, error) {
	var pins []signedRevocationPin
	if path, err := m.signedRevocationsPath(uid); err == nil {
		signedRevocationsMu.Lock()
		r, err := readSignedRevocations(path)
		signedRevocationsMu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		pins = r.Pins
	}
	ownRevoked, err := m.ownRevokedDevices()
	return pins, ownRevoked, err
}

// withoutRevokedApprovers drops revoked devices from a directory listing
// before the approval chain is walked, so a revoked device cannot vouch for
// a device the server lists as approved by it.
func withoutRevokedApprovers(list []netproto.DMDevice, pins []signedRevocationPin, ownRevoked []string) []netproto.DMDevice {
	out := make([]netproto.DMDevice, 0, len(list))
	for _, d := range list {
		if !revokedDevice(d, pins, ownRevoked) {
			out = append(out, d)
		}
	}
	return out
}

func trustedForRevocations(devices []dmTrustedDevice) []devicetrust.Device {
	out := make([]devicetrust.Device, 0, len(devices))
	for _, d := range devices {
		out = append(out, devicetrust.Device{ID: d.DeviceID, Kind: d.Kind, Signing: d.Signing, Encryption: d.X25519, First: d.First})
	}
	return out
}

// applyDeviceRevocations verifies the directory's revocations against the
// verified devices of uid, pins the signed ones, drops every pinned device,
// records the revocations in the secure-messaging store and tells the
// frontend about revocations it has not seen yet. It also returns every pin
// it applied, for the second chain walk without revoked approvers.
func (m *connManager) applyDeviceRevocations(uid string, devices []dmTrustedDevice, listed []netproto.DeviceRevocation) ([]dmTrustedDevice, []signedRevocationPin, error) {
	verified := devicetrust.VerifyRevocations(uid, trustedForRevocations(devices), listed)
	pins, added, err := m.pinSignedRevocations(uid, verified)
	if err != nil {
		return nil, nil, err
	}
	// Revocations verified in this response apply even when they could not be
	// pinned (no pin namespace yet, or the record is full); they are not
	// persisted from here.
	for _, v := range verified {
		pins = append(pins, signedRevocationPin{DeviceID: v.DeviceID, Ed25519Public: v.Ed25519Public, SignerID: v.SignerID, SignedAt: v.SignedAt})
	}
	filtered := withoutPinnedRevocations(devices, pins)
	if len(pins) > 0 {
		if consumer, cerr := m.ratchetConsumerOn(nil); cerr == nil {
			for _, p := range pins {
				if err := consumer.revokeDevice(uid, p.DeviceID); err != nil {
					log.Printf("devices: recording the signed revocation of %s/%s in secure messaging failed (retried with the next device list): %v", uid, p.DeviceID, err)
				}
			}
		}
	}
	m.noticeDeviceRevocations(uid, listed, added)
	return filtered, pins, nil
}

// noticeDeviceRevocations emits one event per revocation this session has not
// reported yet: newly pinned signed revocations, and unsigned (server-only)
// ones the directory lists.
func (m *connManager) noticeDeviceRevocations(uid string, listed []netproto.DeviceRevocation, added []signedRevocationPin) {
	signed := map[string]string{}
	for _, p := range added {
		signed[p.DeviceID] = p.SignerID
	}
	m.mu.Lock()
	if m.revocationNotices == nil {
		m.revocationNotices = map[string]bool{}
		m.revocationBaseline = map[string]bool{}
	}
	// The first list of an account in this session only sets the baseline
	// for server-only revocations: they are not pinned, so reporting them
	// would repeat old revocations on every start.
	baseline := !m.revocationBaseline[uid]
	m.revocationBaseline[uid] = true
	tabID := m.tabID
	var events []DeviceRevokedEvent
	if len(listed) > netproto.MaxListedRevocations {
		listed = listed[:netproto.MaxListedRevocations]
	}
	for _, r := range listed {
		if !netproto.ValidRevocationFields(r) {
			continue
		}
		key := uid + "\x00" + r.DeviceID
		if m.revocationNotices[key] {
			continue
		}
		signer, isSigned := signed[r.DeviceID]
		if r.Signed() && !isSigned {
			continue // signed but not verified, or pinned in an earlier session
		}
		m.revocationNotices[key] = true
		if baseline && !isSigned {
			continue
		}
		events = append(events, DeviceRevokedEvent{TabID: tabID, UniqueID: uid, DeviceID: r.DeviceID, Signed: isSigned, SignerID: signer})
	}
	m.mu.Unlock()
	for _, e := range events {
		m.emit("device_revoked", e)
	}
}
