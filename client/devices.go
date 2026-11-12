package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"

	"noxa/internal/auth"
	"noxa/internal/deviceproof"
	"noxa/internal/netproto"
)

// deviceLinkKeys are the link keys a creating session keeps so it can derive
// the same comparison code (SAS) as the claiming device.
type deviceLinkKeys struct {
	secret []byte
	code   string
}

// DeviceLinkView is a created link as the "PWA / Mobile login"
// dialog shows it. The secret stays in the Go process apart from the URL.
type DeviceLinkView struct {
	LinkID    string `json:"link_id"`
	URL       string `json:"url"`
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
	QR        string `json:"qr"` // PNG data URL of URL
	// LinkHost is the host the QR opens (the server's configured web address);
	// HostMismatch is set when it is not the host this desktop is connected
	// to, so the UI can warn before the user scans it.
	LinkHost     string `json:"link_host"`
	ServerHost   string `json:"server_host"`
	HostMismatch bool   `json:"host_mismatch"`
}

// deviceEventView is a DeviceEvent plus the locally derived SAS.
type deviceEventView struct {
	netproto.DeviceEvent
	SAS         string `json:"sas,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

var errDevicesUnsupported = errors.New("this server does not support linked devices yet")

func (m *connManager) deviceRequest(request netproto.DeviceRequest) (netproto.DeviceResult, error) {
	var result netproto.DeviceResult
	m.mu.Lock()
	supported := m.supportsDevices
	m.mu.Unlock()
	if !supported {
		return result, errDevicesUnsupported
	}
	if !request.Valid() {
		return result, errors.New("invalid device request")
	}
	frame, err := m.request(netproto.MsgDeviceRequest, netproto.MsgDeviceResult, request, 10*time.Second)
	if err != nil {
		return result, err
	}
	if err := netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.Action != request.Action {
		return netproto.DeviceResult{}, errors.New("invalid device response")
	}
	return result, nil
}

// handleDeviceEvent forwards device changes to the webview. Claims of links
// this session created gain the comparison code derived from its own key;
// claims of other links are dropped.
func (m *connManager) handleDeviceEvent(f *netproto.Frame) {
	var event netproto.DeviceEvent
	if err := netproto.Decode(f, &event); err != nil {
		return
	}
	switch event.Event {
	case netproto.DeviceEventLinked, netproto.DeviceEventRevoked, netproto.DeviceEventEndorsed:
		// The account's own device list changed: re-verify it before the
		// next direct message.
		m.mu.Lock()
		ownUID := m.uniqueID
		m.mu.Unlock()
		m.forgetDMDevices(ownUID)
	}
	view := deviceEventView{DeviceEvent: event}
	if event.Event == netproto.DeviceEventLinkClaimed {
		m.mu.Lock()
		keys, ok := m.deviceLinks[event.LinkID]
		m.mu.Unlock()
		if !ok {
			return
		}
		linkKey := keys.secret
		if event.Via == "code" {
			linkKey = []byte(keys.code)
		}
		transcript := netproto.LinkClaimTranscript(event.LinkID, event.Ed25519PublicKey, event.X25519PublicKey)
		view.SAS = netproto.LinkSAS(linkKey, transcript)
		view.Fingerprint = netproto.KeyFingerprint(event.Ed25519PublicKey)
	}
	if event.Event == netproto.DeviceEventLinked && event.Device != nil && len(event.Device.PasskeyProof) > 0 {
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		go guardCrash("passkey device approval", func() { m.approvePasskeyDevices(conn) })
	}
	m.emit("device_event", view)
}

// DeviceView is a device as the "PWA / Mobile login" dialog lists
// it. NeedsConfirm marks a device that is not confirmed for direct messages
// (a phone that signed in with a passkey, another desktop install); this
// desktop can confirm it.
type DeviceView struct {
	netproto.DeviceInfo
	NeedsConfirm bool `json:"needs_confirm,omitempty"`
}

// DevicesForTab lists the account's active linked devices.
func (a *App) DevicesForTab(tabID string) ([]DeviceView, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	result, err := cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if err != nil {
		return nil, err
	}
	cm.rememberCurrentDevice(nil, result.Devices)
	views := make([]DeviceView, 0, len(result.Devices))
	for _, d := range result.Devices {
		views = append(views, DeviceView{DeviceInfo: d})
	}
	cm.markUnconfirmedDevices(views)
	return views, nil
}

// markUnconfirmedDevices flags devices missing from the account's verified
// direct-message devices, when this desktop is itself trusted and could
// confirm them. The verified list (not approved_by) decides: a passkey login
// names the registering device in approved_by but carries no signature.
func (m *connManager) markUnconfirmedDevices(views []DeviceView) {
	if !m.dmDevicesActive() {
		return
	}
	m.mu.Lock()
	ownUID, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	trusted, err := m.dmDeviceList(ownUID, true)
	if err != nil {
		return
	}
	known := make(map[string]bool, len(trusted))
	for _, d := range trusted {
		known[d.DeviceID] = true
	}
	if !known[selfDevice] {
		return
	}
	for i := range views {
		d := views[i].DeviceInfo
		views[i].NeedsConfirm = !d.FirstDevice && !d.Current && d.DeviceID != selfDevice && d.RevokedAt == 0 && !known[d.DeviceID]
	}
}

// ConfirmDeviceForTab endorses an unconfirmed device of this account: this
// trusted desktop signs the device's keys (DeviceApprovalTranscript), so
// contacts accept it for direct messages. The caller passes the signing and
// encryption keys of the record the user reviewed: the device list is fetched
// again from the server before signing, and a record whose keys changed since
// it was shown is refused rather than endorsed.
func (a *App) ConfirmDeviceForTab(tabID, deviceID, ed25519Public, x25519Public string) (netproto.DeviceInfo, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	cm.mu.Lock()
	principal, selfDevice := cm.uniqueID, cm.deviceID
	cm.mu.Unlock()
	if selfDevice == "" {
		return netproto.DeviceInfo{}, errors.New("this desktop is not registered as a device yet")
	}
	if deviceID == "" || deviceID == selfDevice {
		return netproto.DeviceInfo{}, errors.New("choose another device to confirm")
	}
	result, err := cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	var target *netproto.DeviceInfo
	for i := range result.Devices {
		if result.Devices[i].DeviceID == deviceID {
			target = &result.Devices[i]
		}
	}
	if target == nil || target.FirstDevice || target.RevokedAt != 0 {
		return netproto.DeviceInfo{}, errors.New("this device can no longer be confirmed")
	}
	signingKey := target.Ed25519Public
	if signingKey == "" {
		return netproto.DeviceInfo{}, errors.New("this server does not share the device's signing key; update the server to confirm devices")
	}
	if ed25519Public == "" || x25519Public == "" || signingKey != ed25519Public || target.X25519Public != x25519Public {
		return netproto.DeviceInfo{}, errors.New("the device's keys changed since they were shown; review the device again before confirming it")
	}
	if netproto.KeyFingerprint(signingKey) != target.Fingerprint {
		return netproto.DeviceInfo{}, errors.New("the device's key does not match its fingerprint")
	}
	if _, ok := parseDeviceX25519(target.X25519Public); !ok {
		return netproto.DeviceInfo{}, errors.New("the device has no valid encryption key")
	}
	// The same claimed-key validation as a device link: no relayed key
	// field can smuggle a line into the signed transcript.
	if !validClaimedDeviceKeys(signingKey, target.X25519Public) {
		return netproto.DeviceInfo{}, errors.New("the device's keys are not in the expected format")
	}
	id, err := cm.identity()
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	transcript := netproto.DeviceApprovalTranscript(principal, selfDevice, signingKey, target.X25519Public)
	signature, err := auth.SignChallenge(id.PrivateKey, transcript)
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	endorsed, err := cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionEndorse, DeviceID: deviceID, Signature: signature})
	cm.forgetDMDevices(principal)
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	if endorsed.Device == nil {
		return netproto.DeviceInfo{}, errors.New("invalid device response")
	}
	return *endorsed.Device, nil
}

// RenameDeviceForTab renames one of the account's devices.
func (a *App) RenameDeviceForTab(tabID, deviceID, name string) (netproto.DeviceInfo, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	result, err := cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionRename, DeviceID: deviceID, Name: strings.TrimSpace(name)})
	if err != nil {
		return netproto.DeviceInfo{}, err
	}
	if result.Device == nil {
		return netproto.DeviceInfo{}, errors.New("invalid device response")
	}
	return *result.Device, nil
}

// RevokeDeviceForTab signs one of the account's devices out.
func (a *App) RevokeDeviceForTab(tabID, deviceID string) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	req, err := cm.signedRevokeRequest(deviceID)
	if err != nil {
		return err
	}
	if _, err = cm.deviceRequest(req); err != nil {
		return err
	}
	return cm.persistOwnDeviceRevocation(deviceID)
}

// signedRevokeRequest builds a revoke request signed by this desktop over the
// target's current keys (38), so contacts and the account's other devices can
// verify that this account revoked the device. A desktop that is not a
// trusted device yet, or a target without published keys, revokes unsigned:
// the server still revokes it, and clients show it as revoked by the server.
func (m *connManager) signedRevokeRequest(deviceID string) (netproto.DeviceRequest, error) {
	req := netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: deviceID}
	m.mu.Lock()
	principal, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	if principal == "" || selfDevice == "" {
		return req, nil
	}
	list, err := m.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if err != nil {
		return req, err
	}
	var target, self *netproto.DeviceInfo
	for i := range list.Devices {
		switch list.Devices[i].DeviceID {
		case deviceID:
			target = &list.Devices[i]
		case selfDevice:
			self = &list.Devices[i]
		}
	}
	if target == nil {
		return req, errors.New("device not found")
	}
	if self == nil || !self.FirstDevice && !self.Confirmed || target.Ed25519Public == "" ||
		!validClaimedDeviceKeys(target.Ed25519Public, target.X25519Public) {
		return req, nil
	}
	id, err := m.identity()
	if err != nil {
		return req, err
	}
	signedAt := time.Now().Unix()
	transcript := netproto.DeviceRevocationTranscript(principal, selfDevice, deviceID, target.Ed25519Public, target.X25519Public, signedAt, netproto.DeviceRevocationReasonUser)
	signature, err := auth.SignChallenge(id.PrivateKey, transcript)
	if err != nil {
		return req, err
	}
	req.Signature, req.RevokedAt = signature, signedAt
	return req, nil
}

// persistOwnDeviceRevocation records a revocation this user made here,
// independently of server metadata, so a relay replaying the device's earlier
// approval cannot get new messages encrypted to it. The local record filters
// this account's device list for every send path (legacy copies and the
// ratchet's device-key lookup); the ratchet store additionally gets a
// permanent revoked trust record now, or when its next session starts. Only
// locally initiated revocations are persisted: server-sent revocation events
// are not authenticated, and the record is permanent.
func (m *connManager) persistOwnDeviceRevocation(deviceID string) error {
	m.mu.Lock()
	principal, selfDevice := m.uniqueID, m.deviceID
	m.mu.Unlock()
	defer m.forgetDMDevices(principal)
	if deviceID == "" || deviceID == selfDevice || principal == "" {
		return nil
	}
	if err := m.recordOwnDeviceRevocation(deviceID); err != nil {
		return fmt.Errorf("the server signed the device out, but this desktop could not record the revocation: %w", err)
	}
	consumer, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return nil // applied by syncOwnRevocationsOnStart with the next session
	}
	if err := consumer.revokeDevice(principal, deviceID); err != nil {
		log.Printf("devices: recording the revocation of %s in secure messaging failed (retried at the next session): %v", deviceID, err)
	}
	return nil
}

// CreateDeviceLinkForTab starts "PWA / Mobile login".
func (a *App) CreateDeviceLinkForTab(tabID string) (DeviceLinkView, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return DeviceLinkView{}, err
	}
	result, err := cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	if err != nil {
		return DeviceLinkView{}, err
	}
	if result.LinkID == "" || len(result.Secret) != 32 || result.URL == "" || result.Code == "" {
		return DeviceLinkView{}, errors.New("invalid device link response")
	}
	cm.mu.Lock()
	// Only the newest link of this session can be claimed.
	cm.deviceLinks = map[string]deviceLinkKeys{
		result.LinkID: {secret: result.Secret, code: strings.ReplaceAll(result.Code, "-", "")},
	}
	cm.mu.Unlock()
	// The desktop builds the fragment itself (never appends to the
	// server's): link ID, secret and this desktop's own key fingerprint, so
	// the browser that scans this screen can refuse a link the server
	// re-routed to another approver.
	id, err := cm.identity()
	if err != nil {
		return DeviceLinkView{}, err
	}
	base, _, _ := strings.Cut(result.URL, "#")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return DeviceLinkView{}, errors.New("invalid device link response")
	}
	cm.mu.Lock()
	serverHost := connectedHost(cm.addr)
	cm.mu.Unlock()
	fragment := url.Values{"id": {result.LinkID}, "k": {base64.RawURLEncoding.EncodeToString(result.Secret)}, "a": {netproto.KeyFingerprint(id.PublicKey)}}
	result.URL = base + "#" + fragment.Encode()
	image, err := deviceLinkQR(result.URL)
	if err != nil {
		return DeviceLinkView{}, err
	}
	return DeviceLinkView{LinkID: result.LinkID, URL: result.URL, Code: result.Code, ExpiresAt: result.ExpiresAt, QR: image,
		LinkHost: u.Hostname(), ServerHost: serverHost, HostMismatch: !sameHost(u.Hostname(), serverHost)}, nil
}

// connectedHost is the host part of a "host:port" connect address.
func connectedHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// sameHost compares host names case-insensitively, ignoring a trailing dot.
func sameHost(a, b string) bool {
	norm := func(h string) string { return strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".") }
	return a != "" && norm(a) == norm(b)
}

// CancelDeviceLinkForTab withdraws a pending link.
func (a *App) CancelDeviceLinkForTab(tabID, linkID string) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	delete(cm.deviceLinks, linkID)
	cm.mu.Unlock()
	_, err = cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionLinkCancel, LinkID: linkID})
	return err
}

// DecideDeviceLinkForTab approves or denies a claimed link. Approval signs
// the claiming device's keys with this desktop's identity key.
func (a *App) DecideDeviceLinkForTab(tabID, linkID string, approve bool, ed25519Public, x25519Public string) (netproto.DeviceResult, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.DeviceResult{}, err
	}
	cm.mu.Lock()
	_, known := cm.deviceLinks[linkID]
	principal, deviceID := cm.uniqueID, cm.deviceID
	cm.mu.Unlock()
	if !known {
		return netproto.DeviceResult{}, errors.New("this link is no longer pending")
	}
	request := netproto.DeviceRequest{Action: netproto.DeviceActionLinkDecide, LinkID: linkID, Approve: approve}
	if approve && !validClaimedDeviceKeys(ed25519Public, x25519Public) {
		return netproto.DeviceResult{}, errors.New("the claiming device's keys are invalid")
	}
	if approve {
		if deviceID == "" {
			return netproto.DeviceResult{}, errors.New("this desktop is not registered as a device yet")
		}
		id, err := cm.identity()
		if err != nil {
			return netproto.DeviceResult{}, err
		}
		transcript := netproto.DeviceApprovalTranscript(principal, deviceID, ed25519Public, x25519Public)
		if request.Signature, err = auth.SignChallenge(id.PrivateKey, transcript); err != nil {
			return netproto.DeviceResult{}, err
		}
		// Hand this desktop's pinned TLS binding to the linked browser,
		// signed for exactly its keys (its only source of the binding).
		if binding, err := cm.ratchetServerBinding(); err == nil {
			request.ServerBinding = binding
			bound := netproto.DeviceLinkBindingTranscript(principal, deviceID, ed25519Public, x25519Public, binding)
			if request.BindingSignature, err = auth.SignChallenge(id.PrivateKey, bound); err != nil {
				return netproto.DeviceResult{}, err
			}
		}
	}
	result, err := cm.deviceRequest(request)
	cm.mu.Lock()
	delete(cm.deviceLinks, linkID)
	cm.mu.Unlock()
	if approve {
		cm.forgetDMDevices(principal)
	}
	return result, err
}

// deviceLinkQR renders the link URL as a PNG data URL for the dialog. The
// image is generated locally; the link secret never leaves the process
// except inside the QR code itself.
func deviceLinkQR(url string) (string, error) {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return "", err
	}
	code.Scale = 6
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}

// deviceActivityInterval throttles input reports. The server treats a
// desktop as in use for two minutes after one and holds back phone
// notifications meanwhile.
const deviceActivityInterval = time.Minute

// ReportInputActivity tells each connected server that this desktop is in use.
func (a *App) ReportInputActivity() {
	now := time.Now()
	for _, cm := range a.connectedManagers() {
		cm.reportActivity(now)
	}
}

func (a *App) connectedManagers() []*connManager {
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			managers = append(managers, tab.cm)
		}
	}
	return managers
}

func (m *connManager) reportActivity(now time.Time) {
	m.mu.Lock()
	due := m.supportsDevices && m.conn != nil && now.Sub(m.activityReportedAt) >= deviceActivityInterval
	if due {
		m.activityReportedAt = now
	}
	m.mu.Unlock()
	if due {
		_ = m.write(netproto.MsgDeviceRequest, netproto.DeviceRequest{Action: netproto.DeviceActionActivity, Active: true})
	}
}

// ReportReadForTab tells the server this desktop read a conversation
// (scope dm, conversation or channel), so the account's phones clear its
// badge and notification.
func (a *App) ReportReadForTab(tabID, scope, id, messageID string) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	marker := netproto.ReadMarker{Scope: scope, ID: id, MessageID: messageID}
	request := netproto.DeviceRequest{Action: netproto.DeviceActionRead, Read: &marker}
	if !request.Valid() {
		return errors.New("invalid read marker")
	}
	cm.mu.Lock()
	supported := cm.supportsDevices
	cm.mu.Unlock()
	if !supported {
		return nil
	}
	return cm.write(netproto.MsgDeviceRequest, request)
}

// approvePasskeyDevices issues normal device endorsements only after checking a
// key-bound WebAuthn assertion against a delegation signed by a trusted device.
// Every request stays on the captured connection; reconnects cannot cross accounts.
func (m *connManager) approvePasskeyDevices(conn net.Conn) {
	m.mu.Lock()
	due := m.conn == conn && conn != nil && m.supportsDMDevices && !m.isGuest
	principal, self, addr := m.uniqueID, m.deviceID, m.addr
	m.mu.Unlock()
	rpHost := passkeyRPHost(addr)
	if !due || self == "" || rpHost == "" {
		return // no known relying party: passkey devices need manual approval
	}
	pins, ownRevoked, err := m.ownRevocationState(principal)
	if err != nil {
		return // an unreadable revocation record must not count as "none"
	}
	id, err := m.identity()
	if err != nil {
		return
	}
	pinned, err := auth.LoadPublicKey(id.PublicKey)
	if err != nil {
		return
	}
	encryption, _, err := id.x25519()
	if err != nil {
		return
	}
	directory, err := m.requestDeviceDirectory(conn, principal)
	if err != nil {
		return
	}
	// A revoked device cannot vouch here either (audit #6): not in the
	// directory chain, and not inside a passkey certificate's own chain.
	trusted, err := verifyDMDevices(principal, pinned, encryption, withoutRevokedApprovers(directory.Devices, pins, ownRevoked))
	if err != nil {
		return
	}
	// Signed revocations first listed in this response count as well: pin
	// them, then walk the chain again without the approvers they revoke.
	if _, pins, err = m.applyDeviceRevocations(principal, trusted, directory.Revocations); err != nil {
		return
	}
	if trusted, err = verifyDMDevices(principal, pinned, encryption, withoutRevokedApprovers(directory.Devices, pins, ownRevoked)); err != nil {
		return
	}
	trusted = withoutPinnedRevocations(trusted, pins)
	// Server-revoked devices match by signing key too, so a revoked key cannot
	// vouch inside a certificate's chain under a new device ID.
	serverRevoked := map[string]bool{}
	for _, d := range directory.Devices {
		if d.RevokedAt != 0 {
			serverRevoked[d.DeviceID] = true
			pins = append(pins, signedRevocationPin{DeviceID: d.DeviceID, Ed25519Public: d.Ed25519Public})
		}
	}
	revoked := func(d netproto.DMDevice) bool {
		return serverRevoked[d.DeviceID] || revokedDevice(d, pins, ownRevoked)
	}
	known := map[string]bool{}
	for _, d := range trusted {
		known[d.DeviceID] = true
	}
	if !known[self] {
		return
	}
	frame, err := m.requestOn(conn, netproto.MsgDeviceRequest, netproto.MsgDeviceResult, netproto.DeviceRequest{Action: netproto.DeviceActionList}, 10*time.Second)
	if err != nil {
		return
	}
	var list netproto.DeviceResult
	if netproto.Decode(frame, &list) != nil || list.Action != netproto.DeviceActionList {
		return
	}
	for _, target := range list.Devices {
		if target.Kind != "web" || target.DeviceID == self || known[target.DeviceID] || target.FirstDevice || target.RevokedAt != 0 || len(target.PasskeyProof) == 0 {
			continue
		}
		if !validClaimedDeviceKeys(target.Ed25519Public, target.X25519Public) {
			continue
		}
		if deviceproof.Verify(principal, pinned, target.Ed25519Public, target.X25519Public, target.PasskeyProof, rpHost, revoked) != nil {
			continue
		}
		signature, err := auth.SignChallenge(id.PrivateKey, netproto.DeviceApprovalTranscript(principal, self, target.Ed25519Public, target.X25519Public))
		if err != nil {
			continue
		}
		_, err = m.requestOn(conn, netproto.MsgDeviceRequest, netproto.MsgDeviceResult, netproto.DeviceRequest{Action: netproto.DeviceActionEndorse, DeviceID: target.DeviceID, Signature: signature}, 10*time.Second)
		if err != nil {
			return
		}
		m.forgetDMDevices(principal)
	}
}

// passkeyRPHost is the host this desktop dialed, which a passkey
// certificate's relying-party ID must name. An IP address or an unparsable
// address yields "": WebAuthn has no IP relying parties, so auto-endorsement
// stays off and passkey devices need manual approval.
func passkeyRPHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	return host
}

// validClaimedDeviceKeys checks relayed claim keys before the desktop signs
// a transcript over them, so no field can smuggle an extra line into the
// newline-joined approval or binding transcripts. The Ed25519 key is
// canonicalized first and then validated: the PEM must be exactly the
// canonical single-key encoding (what the PWA's publicKeyPEM and Go's
// pem.EncodeToMemory both produce, pinned by mobile/unit/fixtures/interop.json)
// up to benign line-ending formatting (CRLF line breaks, a missing final
// newline). The relayed string itself stays the signed value, because the
// server and every later verifier check the signature over exactly that
// string. The X25519 key must be canonical standard base64 of a non-zero
// 32-byte key.
func validClaimedDeviceKeys(ed25519Public, x25519Public string) bool {
	key, err := auth.LoadPublicKey(ed25519Public)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return false
	}
	canonical := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: key}))
	benign := strings.ReplaceAll(ed25519Public, "\r\n", "\n")
	if !strings.HasSuffix(benign, "\n") {
		benign += "\n"
	}
	if benign != canonical {
		return false
	}
	x, ok := parseDeviceX25519(x25519Public)
	if !ok || strings.ContainsAny(x25519Public, "\r\n") || base64.StdEncoding.EncodeToString(x[:]) != x25519Public {
		return false
	}
	return true
}
