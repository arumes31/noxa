package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"noxa/internal/auth"
	"noxa/internal/e2ee"
)

var dmTrustRootOverride string // isolated test storage
var dmTrustMu sync.Mutex       // every tab/manager shares the same persistent pins
var errDMIdentityChanged = errors.New("contact identity changed; confirm the new fingerprint before messaging or calling")

type dmIdentityPin struct {
	Version     int      `json:"version"`
	Fingerprint string   `json:"fingerprint"`
	Pending     string   `json:"pending,omitempty"`
	Retired     []string `json:"retired,omitempty"`
}

func nativePeerConfirmation(ctx context.Context) func(string, string, string, string) bool {
	if ctx == nil {
		return nil
	}
	return func(server, uid, oldFingerprint, newFingerprint string) bool {
		answer, err := wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
			Type: wailsRuntime.WarningDialog, Title: "Contact identity changed",
			Message: fmt.Sprintf("Contact: %s\nServer: %s\n\nPreviously trusted fingerprint:\n%s\n\nNew fingerprint:\n%s\n\nVerify the new fingerprint with your contact through another trusted channel. Until confirmed, this contact remains blocked.", uid, server, oldFingerprint, newFingerprint),
			Buttons: []string{"Cancel", "Trust new fingerprint"}, DefaultButton: "Cancel", CancelButton: "Cancel",
		})
		return err == nil && answer == "Trust new fingerprint"
	}
}
func (m *connManager) peerPinPath(uid string) (string, string, error) {
	id, err := m.identity()
	if err != nil {
		return "", "", err
	}
	local, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		return "", "", err
	}
	m.mu.Lock()
	addr := m.addr
	m.mu.Unlock()
	server, err := canonicalServerDestination(addr)
	if err != nil || server == "" {
		return "", "", errors.New("server identity unavailable")
	}
	root, err := identityRoot()
	if dmTrustRootOverride != "" {
		root, err = dmTrustRootOverride, nil
	}
	if err != nil {
		return "", "", err
	}
	binding, _ := json.Marshal([]string{hex.EncodeToString(local), server, uid})
	hash := sha256.Sum256(binding)
	return filepath.Join(root, "dm-identities", hex.EncodeToString(hash[:])+".json"), server, nil
}
func readDMPin(path string) (dmIdentityPin, error) {
	var pin dmIdentityPin
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return pin, nil
	}
	if err != nil {
		return pin, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 {
		return pin, errors.New("cannot read contact trust store")
	}
	if json.Unmarshal(raw, &pin) != nil || pin.Version != 1 || len(pin.Fingerprint) != 64 {
		return pin, errors.New("invalid contact trust store")
	}
	if _, err := hex.DecodeString(pin.Fingerprint); err != nil {
		return pin, errors.New("invalid contact fingerprint")
	}
	if len(pin.Retired) > 128 {
		return pin, errors.New("invalid retired identities")
	}
	for _, fp := range pin.Retired {
		if raw, err := hex.DecodeString(fp); err != nil || len(raw) != 32 {
			return pin, errors.New("invalid retired identity")
		}
	}
	if pin.Pending != "" {
		if _, err := e2ee.DecodeKeyRecord(pin.Pending); err != nil {
			return pin, errors.New("invalid pending contact identity")
		}
	}
	return pin, nil
}
func writeDMPin(path string, pin dmIdentityPin) error {
	raw, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(path, raw)
}
func (m *connManager) verifyPeerRecord(uid, encoded string, prompt bool) ([32]byte, error) {
	path, server, err := m.peerPinPath(uid)
	if err != nil {
		return [32]byte{}, err
	}
	return m.verifyPeerRecordAt(uid, encoded, prompt, path, server)
}

// Capture the trust namespace before asynchronous directory work. A reconnect
// must not let a delayed response pin a key under a different server identity.
func (m *connManager) verifyPeerRecordAt(uid, encoded string, prompt bool, path, server string) ([32]byte, error) {
	record, err := e2ee.ParseKeyRecord(encoded, uid)
	if err != nil {
		return [32]byte{}, err
	}
	fingerprint := record.Fingerprint()
	dmTrustMu.Lock()
	pin, err := readDMPin(path)
	if err != nil {
		dmTrustMu.Unlock()
		return [32]byte{}, err
	}
	if pin.Version == 0 {
		pin = dmIdentityPin{Version: 1, Fingerprint: fingerprint}
		err = writeDMPin(path, pin)
		dmTrustMu.Unlock()
		if err != nil {
			return [32]byte{}, err
		}
		return record.PublicKey(), nil
	}
	if pin.Pending == "" && pin.Fingerprint == fingerprint {
		dmTrustMu.Unlock()
		return record.PublicKey(), nil
	}
	if !prompt && slices.Contains(pin.Retired, fingerprint) {
		dmTrustMu.Unlock()
		return [32]byte{}, errDMIdentityChanged
	}
	// A detected change blocks BOTH identities until an explicit decision.
	// Do not replace the pending new identity with a stale cached old key.
	if pin.Fingerprint != fingerprint && pin.Pending != encoded {
		pin.Pending = encoded
		if err = writeDMPin(path, pin); err != nil {
			dmTrustMu.Unlock()
			return [32]byte{}, err
		}
	}
	expected := pin
	dmTrustMu.Unlock()
	if !prompt || m.confirmPeerChange == nil || pin.Pending != encoded || !m.confirmPeerChange(server, uid, pin.Fingerprint, fingerprint) {
		return [32]byte{}, errDMIdentityChanged
	}
	dmTrustMu.Lock()
	defer dmTrustMu.Unlock()
	current, err := readDMPin(path)
	if err != nil {
		return [32]byte{}, err
	}
	if current.Fingerprint != expected.Fingerprint || current.Pending != expected.Pending {
		return [32]byte{}, errors.New("contact identity changed again; retry verification")
	}
	retired := current.Retired
	if !slices.Contains(retired, current.Fingerprint) {
		if len(retired) >= 128 {
			return [32]byte{}, errors.New("contact identity history is full")
		}
		retired = append(retired, current.Fingerprint)
	}
	if err := writeDMPin(path, dmIdentityPin{Version: 1, Fingerprint: fingerprint, Retired: retired}); err != nil {
		return [32]byte{}, err
	}
	return record.PublicKey(), nil
}
func (m *connManager) verifyMessageKey(uid, encoded, ownUID string, ownPub [32]byte) ([32]byte, error) {
	if uid != ownUID {
		return m.verifyPeerRecord(uid, encoded, false)
	}
	record, err := e2ee.ParseKeyRecord(encoded, uid)
	if err != nil {
		return [32]byte{}, err
	}
	id, err := m.identity()
	if err != nil {
		return [32]byte{}, err
	}
	signing, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil || !bytes.Equal(signing, record.SigningKey) || record.PublicKey() != ownPub {
		return [32]byte{}, errors.New("message belongs to another identity")
	}
	return record.PublicKey(), nil
}
func (c *pubKeyCache) recordFor(uid string, key [32]byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.keys[uid]
	if !ok || entry.key != key || !entry.expiresAt.After(c.now()) {
		return ""
	}
	return entry.record
}
func (m *connManager) verifiedCachedPeer(uid string, key [32]byte) bool {
	encoded := m.pubKeys.recordFor(uid, key)
	record, err := e2ee.ParseKeyRecord(encoded, uid)
	path, _, pathErr := m.peerPinPath(uid)
	trusted := false
	if err == nil && pathErr == nil && record.PublicKey() == key {
		dmTrustMu.Lock()
		pin, err := readDMPin(path)
		dmTrustMu.Unlock()
		trusted = err == nil && pin.Pending == "" && pin.Fingerprint == record.Fingerprint()
	}
	if !trusted {
		m.pubKeys.forgetKey(uid, key)
	}
	return trusted
}
