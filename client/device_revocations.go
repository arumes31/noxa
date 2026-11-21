// device_revocations.go keeps the account's own device revocations made on
// this desktop. The record lives next to the contact trust pins (per local
// identity and server), independent of server metadata and of whether a
// secure-messaging session is running, so every send path can honour it:
// the legacy per-device copies filter the device list through it, and the
// ratchet store receives it as a permanent revoked trust record.
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
	"time"

	"noxa/internal/auth"
)

var ownRevocationsMu sync.Mutex

type ownDeviceRevocations struct {
	Version int      `json:"version"`
	Devices []string `json:"devices"`
	// SigningKeys holds, per revoked device ID, the signing key this desktop
	// had verified for the device before it was revoked. A device ID is not
	// part of what an approval signs, so the key is what identifies the
	// device. Records written before this field existed name IDs only.
	SigningKeys map[string][]byte `json:"signing_keys,omitempty"`
}

// names reports whether the record revokes a device, by its ID or by its
// signing key under any ID.
func (r ownDeviceRevocations) names(deviceID string, signing ed25519.PublicKey) bool {
	if slices.Contains(r.Devices, deviceID) {
		return true
	}
	if len(signing) != ed25519.PublicKeySize {
		return false
	}
	for _, key := range r.SigningKeys {
		if bytes.Equal(key, signing) {
			return true
		}
	}
	return false
}

// ownRevocationsPath names the record for this identity, server and account.
func (m *connManager) ownRevocationsPath() (string, error) {
	id, err := m.identity()
	if err != nil {
		return "", err
	}
	local, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	addr, principal := m.addr, m.uniqueID
	m.mu.Unlock()
	server, err := canonicalServerDestination(addr)
	if err != nil || server == "" || principal == "" {
		return "", errors.New("server identity unavailable")
	}
	if identityDeleted(id.PublicKey) {
		return "", errIdentityDeleted
	}
	return dmPinPathFor(local, server, "\x00own-device-revocations\x00"+principal)
}

func readOwnRevocations(path string) (ownDeviceRevocations, error) {
	var r ownDeviceRevocations
	raw, err := os.ReadFile(path) // #nosec G304 -- application-owned path derived from hashed identity data.
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, errors.New("corrupt device revocation record")
	}
	return r, nil
}

// recordOwnDeviceRevocation durably adds deviceID, and its signing key when
// one is known, to the local record.
func (m *connManager) recordOwnDeviceRevocation(deviceID string, signing ed25519.PublicKey) error {
	path, err := m.ownRevocationsPath()
	if err != nil {
		return err
	}
	ownRevocationsMu.Lock()
	defer ownRevocationsMu.Unlock()
	r, err := readOwnRevocations(path)
	if err != nil {
		return err
	}
	if slices.Contains(r.Devices, deviceID) {
		return nil
	}
	r.Version = 1
	r.Devices = append(r.Devices, deviceID)
	if len(signing) == ed25519.PublicKeySize {
		if r.SigningKeys == nil {
			r.SigningKeys = map[string][]byte{}
		}
		r.SigningKeys[deviceID] = signing
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(path, raw)
}

// ownRevokedDevices returns the devices revoked from this desktop. An
// unreadable record is an error: callers must not treat it as "none".
func (m *connManager) ownRevokedDevices() (ownDeviceRevocations, error) {
	path, err := m.ownRevocationsPath()
	if err != nil {
		// Without an identity/server namespace nothing can have been recorded.
		return ownDeviceRevocations{}, nil
	}
	ownRevocationsMu.Lock()
	defer ownRevocationsMu.Unlock()
	return readOwnRevocations(path)
}

// verifiedOwnDeviceKey returns the signing key of one of this account's
// devices from the device list this desktop verified earlier, or nil. It
// never asks the server: a list fetched while a device is being revoked must
// not decide which key the revocation names. The account identity key is
// never returned, since it anchors every other device.
func (m *connManager) verifiedOwnDeviceKey(deviceID string) ed25519.PublicKey {
	m.mu.Lock()
	principal := m.uniqueID
	m.mu.Unlock()
	id, err := m.identity()
	if err != nil {
		return nil
	}
	root, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		return nil
	}
	devices, _, _ := m.dmDevices.get(principal, time.Now())
	for _, d := range devices {
		if d.DeviceID == deviceID && !d.First && !bytes.Equal(d.Signing, root) {
			return d.Signing
		}
	}
	return nil
}

// withoutOwnRevokedDevices drops locally revoked devices from this account's
// verified device list, whatever approvals the directory still serves and
// whatever device ID it lists their keys under.
func (m *connManager) withoutOwnRevokedDevices(devices []dmTrustedDevice) ([]dmTrustedDevice, error) {
	revoked, err := m.ownRevokedDevices()
	if err != nil || len(revoked.Devices) == 0 {
		return devices, err
	}
	out := devices[:0:0]
	for _, d := range devices {
		key := d.Signing
		if d.First {
			key = nil // the account identity is never revoked by key
		}
		if !revoked.names(d.DeviceID, key) {
			out = append(out, d)
		}
	}
	return out, nil
}

// syncOwnRevocationsToRatchet writes every recorded revocation into the
// secure-messaging store; it runs when a session starts, so a revocation made
// while no session was running is applied before the first send.
func (m *connManager) syncOwnRevocationsToRatchet(consumer *ratchetConsumer) error {
	m.mu.Lock()
	principal := m.uniqueID
	m.mu.Unlock()
	revoked, err := m.ownRevokedDevices()
	if err != nil {
		return err
	}
	for _, device := range revoked.Devices {
		if err := consumer.revokeDevice(principal, device); err != nil {
			return err
		}
	}
	return nil
}

func (m *connManager) syncOwnRevocationsOnStart(consumer *ratchetConsumer) {
	if err := m.syncOwnRevocationsToRatchet(consumer); err != nil {
		log.Printf("devices: applying recorded revocations to secure messaging failed: %v", err)
	}
}
