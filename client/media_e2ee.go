package main

// media_e2ee.go: the desktop bridge for encrypted channel media (Task 2).
//
// Frame encryption and the key epochs live in the frontend (WebView
// encoded-transform worker, media-keys.js). The native side only carries
// the media keys between devices over the authenticated ratchet control
// transport (ratchet_control.go):
//
//   - SendMediaKeyForTab seals one "media-key" control for exactly one
//     target device. Nothing is logged or kept at rest (controls keep only
//     their disposition; the data never is).
//   - Delivered "media-key" controls are emitted to the frontend as the
//     "media_key" event of the owning tab. That event is never journaled,
//     never projected into chat, activity, notifications or history, and
//     background tabs drop it (voice is active-tab only).
//   - WebRTCOfferSecureForTab declares the encrypted transform on every
//     channel offer (netproto.MediaE2EEVersion).

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
)

const mediaKeyControlKind = "media-key"

// mediaKeyEvent is the frontend's view of one delivered media-key control.
type mediaKeyEvent struct {
	Sender       string                 `json:"sender"`
	SenderDevice string                 `json:"sender_device"`
	Kind         string                 `json:"kind"`
	Scope        admission.ControlScope `json:"scope"`
	Epoch        int64                  `json:"epoch"`
	ExpiresAt    int64                  `json:"expires_at"`
	Data         json.RawMessage        `json:"data,omitempty"`
}

// relayMediaKey forwards delivered media-key controls of this connection to
// the frontend; other control kinds are not media.
func (m *connManager) relayMediaKey(ctl RatchetControl) {
	if ctl.Kind != mediaKeyControlKind || len(ctl.Data) == 0 {
		return
	}
	m.emit("media_key", mediaKeyEvent{Sender: ctl.Sender, SenderDevice: ctl.SenderDevice, Kind: ctl.Kind, Scope: ctl.Scope,
		Epoch: ctl.Epoch, ExpiresAt: ctl.ExpiresAt, Data: ctl.Data})
}

// MediaKeyRequest is one media-key control to send from the frontend. Data
// is the control's JSON data object exactly as the frontend built it.
type MediaKeyRequest struct {
	LogicalID string                 `json:"logical_id"`
	Principal string                 `json:"principal"`
	Device    string                 `json:"device"`
	Kind      string                 `json:"kind"`
	Scope     admission.ControlScope `json:"scope"`
	Epoch     int64                  `json:"epoch"`
	ExpiresAt int64                  `json:"expires_at"`
	Data      json.RawMessage        `json:"data"`
}

// MediaKeyResult reports the admission state ("accepted") or a coded error.
type MediaKeyResult struct {
	State string `json:"state,omitempty"`
	Code  string `json:"code,omitempty"`
}

// SendMediaKeyForTab sends one media-key control on that tab's connection.
func (a *App) SendMediaKeyForTab(tabID string, req MediaKeyRequest) (MediaKeyResult, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return MediaKeyResult{}, err
	}
	return cm.sendMediaKey(req)
}

func (m *connManager) sendMediaKey(req MediaKeyRequest) (MediaKeyResult, error) {
	if req.Kind != mediaKeyControlKind || len(req.Data) == 0 {
		return MediaKeyResult{Code: "invalid"}, errors.New("invalid")
	}
	// Only to a current media device of this session's own voice channel.
	if err := m.checkMediaKeyTarget(req); err != nil {
		return MediaKeyResult{Code: "not_recipient"}, err
	}
	out, err := m.SendRatchetControl(RatchetControlIntent{LogicalID: req.LogicalID, Principal: req.Principal, Device: req.Device,
		Body: admission.ControlBody{Kind: req.Kind, Scope: req.Scope, Epoch: req.Epoch, ExpiresAt: req.ExpiresAt, Data: req.Data}})
	if err != nil {
		code := ratchetCode(err)
		if code == "" {
			code = "unavailable"
		}
		return MediaKeyResult{Code: code}, errors.New(code)
	}
	if out.Receipt == nil {
		return MediaKeyResult{Code: "unavailable"}, errors.New("unavailable")
	}
	return MediaKeyResult{State: out.Receipt.State}, nil
}

// MediaE2EEStatus tells the frontend whether and as whom it can publish
// encrypted channel media on a tab.
type MediaE2EEStatus struct {
	// Supported: the server requires and supports encrypted channel media.
	Supported bool   `json:"supported"`
	Principal string `json:"principal,omitempty"`
	Device    string `json:"device,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	// Reason explains why encrypted media cannot start ("" when it can):
	// server_outdated, keyless_guest, no_device, secure_unavailable.
	Reason string `json:"reason,omitempty"`
}

// MediaE2EEStatusForTab reports the encrypted media identity of a tab.
func (a *App) MediaE2EEStatusForTab(tabID string) (MediaE2EEStatus, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return MediaE2EEStatus{}, err
	}
	return cm.mediaE2EEStatus(), nil
}

func (m *connManager) mediaE2EEStatus() MediaE2EEStatus {
	m.mu.Lock()
	st := MediaE2EEStatus{Supported: m.supportsMediaE2EE, Principal: m.uniqueID, Device: m.deviceID, ClientID: m.clientID}
	keyless := m.keyless || strings.HasPrefix(m.uniqueID, "guest:")
	conn := m.conn
	m.mu.Unlock()
	switch {
	case !st.Supported:
		st.Reason = "server_outdated"
	case keyless:
		st.Reason = "keyless_guest"
	case st.Device == "":
		st.Reason = "no_device"
	default:
		if _, err := m.ratchetConsumerOn(conn); err != nil {
			st.Reason = "secure_unavailable"
		}
	}
	return st
}

// olmPendingEvent is the server's content-free "deliveries pending" doorbell
// (internal/server/olm_rate.go): it wakes the ratchet drain immediately, so
// media-key controls (rekeys) and messages are fetched when they exist
// instead of by fast polling. It is never shown or forwarded.
const olmPendingEvent = "olm_pending"

// doorbellDrainInterval spaces doorbell-triggered drains.
const doorbellDrainInterval = 250 * time.Millisecond

// isOlmDoorbell reports whether an event frame is the doorbell.
func isOlmDoorbell(payload []byte) bool {
	var env struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &env) == nil && env.Type == olmPendingEvent
}

// ringRatchet wakes the drain loop (never blocks; one pending wake is enough).
func (m *connManager) ringRatchet() {
	select {
	case m.ratchetWake <- struct{}{}:
	default:
	}
}

// mediaE2EESupported records the server's capability at authentication.
func mediaE2EESupported(caps []string) bool {
	return slices.Contains(caps, netproto.CapabilityMediaE2EE)
}

// WebRTCOfferSecureForTab sends a channel offer that declares the
// encrypted transform. The frontend calls it only after every sender and
// receiver of the peer carries the transform.
func (a *App) WebRTCOfferSecureForTab(tabID, sdp string, tracks []netproto.TrackSlot, version string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	if version != netproto.MediaE2EEVersion {
		return "", errors.New("unsupported encrypted media version")
	}
	return cm.webRTCOfferWith(netproto.WebRTCOffer{SDP: sdp, Tracks: tracks, MediaE2EE: version})
}
