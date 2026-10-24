// dm_devices.go is the client half of direct messages to every device
// (dm_devices_v1). The key directory lists an account's devices; this client
// trusts a device only when its keys chain to the account's pinned identity:
// the first device carries the identity's own signing key, and every other
// device carries an approval signature by a device already trusted. A direct
// message is then sealed once per trusted device of the recipient and of this
// account, so the user's phones and other desktops can read it too.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

// dmDeviceTTL bounds how long a verified device list is reused. A device the
// list misses is caught by the server (RecipientDevicesStale) and refreshed.
const dmDeviceTTL = 5 * time.Minute

// errDMDevicesUnverified means the account's first device does not carry the
// pinned identity key: the list is never used and the legacy single copy to
// the pinned account key is sent instead.
var errDMDevicesUnverified = errors.New("the contact's device list does not match their verified identity")

// dmTrustedDevice is one device whose keys chain to the account identity.
type dmTrustedDevice struct {
	DeviceID string
	Kind     string
	X25519   [32]byte
}

type dmDeviceListEntry struct {
	devices   []dmTrustedDevice
	expiresAt time.Time
}

// dmDeviceCache holds verified device lists per account. Its zero value is
// ready; clear drops every list (reconnect) and bumps the epoch so a fetch
// that started before cannot repopulate it.
type dmDeviceCache struct {
	mu      sync.Mutex
	entries map[string]dmDeviceListEntry
	epoch   uint64
}

func (c *dmDeviceCache) get(uid string, now time.Time) ([]dmTrustedDevice, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[uid]
	if !ok || !entry.expiresAt.After(now) {
		return nil, c.epoch, false
	}
	return entry.devices, c.epoch, true
}

func (c *dmDeviceCache) put(uid string, devices []dmTrustedDevice, epoch uint64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]dmDeviceListEntry)
	}
	if len(c.entries) >= pubKeyCacheCapacity {
		clear(c.entries)
	}
	c.entries[uid] = dmDeviceListEntry{devices: devices, expiresAt: now.Add(dmDeviceTTL)}
}

func (c *dmDeviceCache) forget(uids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, uid := range uids {
		delete(c.entries, uid)
	}
}

func (c *dmDeviceCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
	c.epoch++
}

// verifyDMDevices returns the devices of principal whose keys chain to its
// identity: signing is the pinned Ed25519 key and encryption the X25519 key of
// the account's signed key record. The first device must carry signing, or
// the whole list is refused (errDMDevicesUnverified). Every other device needs
// an approval signature by a trusted device over DeviceApprovalTranscript;
// forged, dangling or cyclic approvals leave the device out.
func verifyDMDevices(principal string, signing ed25519.PublicKey, encryption [32]byte, devices []netproto.DMDevice) ([]dmTrustedDevice, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	byID := make(map[string]netproto.DMDevice, len(devices))
	var first *netproto.DMDevice
	for i := range devices {
		d := devices[i]
		if d.DeviceID == "" {
			continue
		}
		if _, dup := byID[d.DeviceID]; dup {
			return nil, errDMDevicesUnverified
		}
		byID[d.DeviceID] = d
		if d.FirstDevice {
			if first != nil {
				return nil, errDMDevicesUnverified
			}
			first = &devices[i]
		}
	}
	if first == nil {
		return nil, errDMDevicesUnverified
	}
	firstKey, err := auth.LoadPublicKey(first.Ed25519Public)
	if err != nil || len(signing) != ed25519.PublicKeySize || !bytes.Equal(firstKey, signing) {
		return nil, errDMDevicesUnverified
	}
	if x, ok := parseDeviceX25519(first.X25519Public); !ok || x != encryption {
		// The first device's encryption key is the account record's; a
		// directory that disagrees cannot vouch for anything else.
		log.Printf("dm devices: first device of %s lists another encryption key; using the signed record only", principal)
		return []dmTrustedDevice{{DeviceID: first.DeviceID, Kind: first.Kind, X25519: encryption}}, nil
	}

	const (
		unknown = iota
		visiting
		trusted
		untrusted
	)
	state := make(map[string]int, len(byID))
	state[first.DeviceID] = trusted
	var resolve func(id string) bool
	resolve = func(id string) bool {
		switch state[id] {
		case trusted:
			return true
		case untrusted, visiting: // visiting again is a cycle
			return false
		}
		state[id] = visiting
		d := byID[id]
		ok := false
		if approver, known := byID[d.ApprovedBy]; known && d.ApprovedBy != id && len(d.ApprovalSignature) == ed25519.SignatureSize && resolve(d.ApprovedBy) {
			if key, err := auth.LoadPublicKey(approver.Ed25519Public); err == nil {
				transcript := netproto.DeviceApprovalTranscript(principal, d.ApprovedBy, d.Ed25519Public, d.X25519Public)
				ok = ed25519.Verify(key, transcript, d.ApprovalSignature)
			}
		}
		if _, valid := parseDeviceX25519(d.X25519Public); !valid {
			ok = false
		}
		if ok {
			state[id] = trusted
		} else {
			state[id] = untrusted
		}
		return ok
	}
	out := []dmTrustedDevice{{DeviceID: first.DeviceID, Kind: first.Kind, X25519: encryption}}
	for _, d := range devices {
		if d.DeviceID == "" || d.DeviceID == first.DeviceID || !resolve(d.DeviceID) {
			continue
		}
		x, _ := parseDeviceX25519(d.X25519Public)
		out = append(out, dmTrustedDevice{DeviceID: d.DeviceID, Kind: d.Kind, X25519: x})
	}
	return out, nil
}

// parseDeviceX25519 decodes a directory X25519 key (base64 of 32 bytes).
func parseDeviceX25519(encoded string) ([32]byte, bool) {
	var key [32]byte
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != len(key) || bytes.Equal(raw, key[:]) {
		return key, false
	}
	copy(key[:], raw)
	return key, true
}

// dmDevicesActive reports whether this session sends per-device copies: the
// server routes them and this desktop is a registered device.
func (m *connManager) dmDevicesActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.supportsDMDevices && m.deviceID != ""
}

// dmDeviceList returns the verified devices of uid, from the cache unless
// force. A peer's list is checked against the pinned identity (never prompts:
// the send path pins through lookupPeerPubKey first); this account's own list
// is checked against this identity.
func (m *connManager) dmDeviceList(uid string, force bool) ([]dmTrustedDevice, error) {
	now := time.Now()
	devices, epoch, ok := m.dmDevices.get(uid, now)
	if ok && !force {
		return devices, nil
	}
	id, err := m.identity()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	conn, ownUID := m.conn, m.uniqueID
	m.mu.Unlock()
	var pinPath, pinServer string
	if uid != ownUID {
		if pinPath, pinServer, err = m.peerPinPath(uid); err != nil {
			return nil, err
		}
	}
	resp, err := m.requestDeviceDirectory(conn, uid)
	if err != nil {
		return nil, err
	}
	var signing ed25519.PublicKey
	var encryption [32]byte
	if uid == ownUID {
		if signing, err = auth.LoadPublicKey(id.PublicKey); err != nil {
			return nil, err
		}
		if encryption, _, err = id.x25519(); err != nil {
			return nil, err
		}
	} else if len(resp.Devices) > 0 {
		if resp.PublicKey == "" {
			return nil, errDMDevicesUnverified
		}
		if encryption, err = m.verifyPeerRecordAt(uid, resp.PublicKey, false, pinPath, pinServer); err != nil {
			return nil, err
		}
		record, err := e2ee.ParseKeyRecord(resp.PublicKey, uid)
		if err != nil {
			return nil, err
		}
		signing = record.SigningKey
	}
	devices, err = verifyDMDevices(uid, signing, encryption, resp.Devices)
	if err != nil {
		log.Printf("dm devices: refusing the device list of %s: %v", uid, err)
		return nil, err
	}
	m.dmDevices.put(uid, devices, epoch, now)
	return devices, nil
}

// requestDeviceDirectory asks the key directory for uid's record and device
// list, honouring one short rate-limit pause.
func (m *connManager) requestDeviceDirectory(conn net.Conn, uid string) (netproto.KeyResponse, error) {
	var resp netproto.KeyResponse
	for attempt := 0; ; attempt++ {
		f, err := m.requestOn(conn, netproto.MsgKeyRequest, netproto.MsgKeyResponse, netproto.KeyRequest{UniqueID: uid, Devices: true}, 5*time.Second)
		if err == nil {
			if err := netproto.Decode(f, &resp); err != nil || resp.UniqueID != uid {
				return netproto.KeyResponse{}, errors.New("invalid device directory response")
			}
			return resp, nil
		}
		var failure *requestFailure
		if attempt > 0 || !errors.As(err, &failure) || failure.response.RetryAfterMS <= 0 || failure.response.RetryAfterMS > 5_000 {
			return resp, err
		}
		time.Sleep(time.Duration(failure.response.RetryAfterMS) * time.Millisecond)
	}
}

// buildDMCopies seals text once per recipient device and once per own device
// (this device included, so the server echoes the sender's copy). Recipient
// copies are mandatory; own copies beyond MaxDMCopies are dropped, this
// device's last.
func buildDMCopies(text string, senderPriv [32]byte, recipients, own []dmTrustedDevice, selfDeviceID string) ([]netproto.DMCopy, error) {
	seen := make(map[string]bool)
	targets := make([]dmTrustedDevice, 0, len(recipients)+len(own))
	add := func(d dmTrustedDevice) {
		if d.DeviceID != "" && !seen[d.DeviceID] {
			seen[d.DeviceID] = true
			targets = append(targets, d)
		}
	}
	for _, d := range recipients {
		add(d)
	}
	for _, d := range own {
		if d.DeviceID == selfDeviceID {
			add(d)
		}
	}
	for _, d := range own {
		add(d)
	}
	if len(recipients) == 0 || len(recipients) > netproto.MaxDMCopies {
		return nil, errors.New("cannot address this contact's devices; try again")
	}
	if len(targets) > netproto.MaxDMCopies {
		targets = targets[:netproto.MaxDMCopies]
	}
	copies := make([]netproto.DMCopy, 0, len(targets))
	for _, d := range targets {
		blob, err := sealDM(text, d.X25519, senderPriv)
		if err != nil {
			return nil, err
		}
		copies = append(copies, netproto.DMCopy{DeviceID: d.DeviceID, Text: blob})
	}
	return copies, nil
}

// encryptDirectCopies builds a per-device ChatSend for uid. ok=false selects
// the legacy single copy: the recipient lists no devices, its list cannot be
// verified, or this desktop is not a trusted device of its own account.
func (m *connManager) encryptDirectCopies(uid, text string, pub, priv [32]byte) (netproto.ChatSend, bool, error) {
	m.mu.Lock()
	ownUID, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	recipients, err := m.dmDeviceList(uid, false)
	if errors.Is(err, errDMDevicesUnverified) || (err == nil && len(recipients) == 0) {
		return netproto.ChatSend{}, false, nil
	}
	if err != nil {
		return netproto.ChatSend{}, false, err
	}
	own := recipients
	if uid != ownUID {
		if own, err = m.dmDeviceList(ownUID, false); err != nil {
			if errors.Is(err, errDMDevicesUnverified) {
				return netproto.ChatSend{}, false, nil
			}
			return netproto.ChatSend{}, false, err
		}
	}
	self := false
	for _, d := range own {
		self = self || (d.DeviceID == selfDevice && d.X25519 == pub)
	}
	if !self {
		return netproto.ChatSend{}, false, nil
	}
	copies, err := buildDMCopies(text, priv, recipients, own, selfDevice)
	if err != nil {
		return netproto.ChatSend{}, false, err
	}
	return netproto.ChatSend{ToUniqueID: uid, Enc: true, ClientMsgID: newClientMsgID(), Copies: copies}, true, nil
}

// forgetDMDevices drops cached device lists, e.g. after the server reported
// them stale or the account's own devices changed.
func (m *connManager) forgetDMDevices(uids ...string) {
	m.dmDevices.forget(uids...)
}

// trustedSenderDevice reports whether deviceID with key is a trusted device
// of uid. This device's own echo is trusted directly; an unknown device
// refreshes the list once, since it may have just been linked.
func (m *connManager) trustedSenderDevice(uid, deviceID string, key, ownPub [32]byte) bool {
	m.mu.Lock()
	ownUID, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	if uid == ownUID && deviceID == selfDevice && selfDevice != "" {
		return key == ownPub
	}
	for _, force := range []bool{false, true} {
		devices, err := m.dmDeviceList(uid, force)
		if err != nil {
			return false
		}
		for _, d := range devices {
			if d.DeviceID == deviceID {
				return d.X25519 == key
			}
		}
	}
	return false
}

// decryptDeviceCopy opens a per-device copy addressed to this device. The
// sender's device must be a trusted device of FromUniqueID; a copy from one
// of this account's other devices (FromUniqueID is this account) is a
// message the user sent elsewhere and is shown as outgoing to ToUniqueID.
func (m *connManager) decryptDeviceCopy(chat netproto.ChatBroadcast, pub, priv [32]byte) string {
	m.mu.Lock()
	selfDevice := m.deviceID
	m.mu.Unlock()
	if selfDevice == "" || chat.RecipientDeviceID != selfDevice || chat.FromUniqueID == "" || chat.SenderDeviceID == "" {
		return missingKeyText
	}
	senderKey, ok := parseDeviceX25519(chat.SenderDeviceKey)
	if !ok || !m.trustedSenderDevice(chat.FromUniqueID, chat.SenderDeviceID, senderKey, pub) {
		return missingKeyText
	}
	plain, err := openDM(chat.Text, senderKey, priv)
	if err != nil {
		return missingKeyText
	}
	return plain
}

// ensureDeviceRegistered lists the account's devices once after connecting,
// which registers a desktop with the server (devices_v1) and records this
// session's device id.
func (m *connManager) ensureDeviceRegistered(conn net.Conn) {
	m.mu.Lock()
	due := m.supportsDevices && !m.isGuest && m.deviceID == "" && m.conn == conn
	m.mu.Unlock()
	if !due {
		return
	}
	result, err := m.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if err != nil {
		log.Printf("device registration failed: %v", err)
		return
	}
	m.rememberCurrentDevice(conn, result.Devices)
}

// rememberCurrentDevice records the session's device from a device list.
func (m *connManager) rememberCurrentDevice(conn net.Conn, devices []netproto.DeviceInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deviceID != "" || (conn != nil && m.conn != conn) {
		return
	}
	for _, d := range devices {
		if d.Current {
			m.deviceID = d.DeviceID
		}
	}
}
