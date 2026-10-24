package main

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"rsc.io/qr"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// deviceLinkKeys are the link keys a creating session keeps so it can derive
// the same comparison code (SAS) as the claiming device.
type deviceLinkKeys struct {
	secret []byte
	code   string
}

// DeviceLinkView is a created link as the "Other login for this server"
// dialog shows it. The secret stays in the Go process apart from the URL.
type DeviceLinkView struct {
	LinkID    string `json:"link_id"`
	URL       string `json:"url"`
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
	QR        string `json:"qr"` // PNG data URL of URL
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
	m.emit("device_event", view)
}

// DeviceView is a device as the "Other login for this server" dialog lists
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
// contacts accept it for direct messages.
func (a *App) ConfirmDeviceForTab(tabID, deviceID string) (netproto.DeviceInfo, error) {
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
	if netproto.KeyFingerprint(signingKey) != target.Fingerprint {
		return netproto.DeviceInfo{}, errors.New("the device's key does not match its fingerprint")
	}
	if _, ok := parseDeviceX25519(target.X25519Public); !ok {
		return netproto.DeviceInfo{}, errors.New("the device has no valid encryption key")
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
	_, err = cm.deviceRequest(netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: deviceID})
	return err
}

// CreateDeviceLinkForTab starts "Other login for this server".
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
	image, err := deviceLinkQR(result.URL)
	if err != nil {
		return DeviceLinkView{}, err
	}
	return DeviceLinkView{LinkID: result.LinkID, URL: result.URL, Code: result.Code, ExpiresAt: result.ExpiresAt, QR: image}, nil
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
