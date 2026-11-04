// device_revocations.go keeps the account's own device revocations made on
// this desktop. The record lives next to the contact trust pins (per local
// identity and server), independent of server metadata and of whether a
// secure-messaging session is running, so every send path can honour it:
// the legacy per-device copies filter the device list through it, and the
// ratchet store receives it as a permanent revoked trust record.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"sync"

	"noxa/internal/auth"
)

var ownRevocationsMu sync.Mutex

type ownDeviceRevocations struct {
	Version int      `json:"version"`
	Devices []string `json:"devices"`
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

// recordOwnDeviceRevocation durably adds deviceID to the local record.
func (m *connManager) recordOwnDeviceRevocation(deviceID string) error {
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
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(path, raw)
}

// ownRevokedDevices returns the devices revoked from this desktop. An
// unreadable record is an error: callers must not treat it as "none".
func (m *connManager) ownRevokedDevices() ([]string, error) {
	path, err := m.ownRevocationsPath()
	if err != nil {
		// Without an identity/server namespace nothing can have been recorded.
		return nil, nil
	}
	ownRevocationsMu.Lock()
	defer ownRevocationsMu.Unlock()
	r, err := readOwnRevocations(path)
	return r.Devices, err
}

// withoutOwnRevokedDevices drops locally revoked devices from this account's
// verified device list, whatever approvals the directory still serves.
func (m *connManager) withoutOwnRevokedDevices(devices []dmTrustedDevice) ([]dmTrustedDevice, error) {
	revoked, err := m.ownRevokedDevices()
	if err != nil || len(revoked) == 0 {
		return devices, err
	}
	out := devices[:0:0]
	for _, d := range devices {
		if !slices.Contains(revoked, d.DeviceID) {
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
	for _, device := range revoked {
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
