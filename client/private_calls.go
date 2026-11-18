package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"noxa/internal/e2ee"
	"noxa/internal/netproto"
)

type PrivateCallDescription struct {
	CallID string `json:"call_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"`
	SDP    string `json:"sdp"`
	// Sub names a sub-connection: callSubScreen from a native screen sender
	// (offer, candidates), callSubScreenReply from its viewer (answer,
	// candidates). Empty is the call's main connection.
	Sub string `json:"sub,omitempty"`
	// Features is what the sender of a main offer or answer can receive.
	Features []string `json:"features,omitempty"`
}

// Native screen sub-connection (one per sharing peer and viewer). Only sent
// to a peer whose main offer or answer listed callFeatureScreenSub: older
// apps would apply a sub-connection offer to the main connection.
const (
	callFeatureScreenSub = "screen-sub"
	callSubScreen        = "screen"
	callSubScreenReply   = "screen-reply"
)

// callFeatures is what this app advertises in main offers and answers.
var callFeatures = []string{callFeatureScreenSub}

// validCallDescriptionShape checks the sub-connection and feature fields.
func validCallDescriptionShape(d PrivateCallDescription) bool {
	if len(d.Features) > 8 {
		return false
	}
	for _, f := range d.Features {
		if f == "" || len(f) > 32 {
			return false
		}
	}
	switch d.Sub {
	case "":
		return true
	case callSubScreen:
		return len(d.Features) == 0 && (d.Type == "offer" || d.Type == "candidates")
	case callSubScreenReply:
		return len(d.Features) == 0 && (d.Type == "answer" || d.Type == "candidates")
	default:
		return false
	}
}

func validPrivateCallPayload(kind, payload string) bool {
	if len(payload) == 0 || len(payload) > 60000 {
		return false
	}
	if kind == "offer" || kind == "answer" {
		return true
	}
	if kind != "candidates" || len(payload) > 32768 {
		return false
	}
	type candidate struct {
		Candidate        *string `json:"candidate"`
		SDPMid           *string `json:"sdpMid"`
		SDPMLineIndex    *uint16 `json:"sdpMLineIndex"`
		UsernameFragment *string `json:"usernameFragment"`
	}
	var candidates []*candidate
	if err := json.Unmarshal([]byte(payload), &candidates); err != nil || len(candidates) == 0 || len(candidates) > 16 {
		return false
	}
	for _, entry := range candidates {
		if entry == nil || entry.Candidate == nil || len(*entry.Candidate) > 2048 ||
			!strings.HasPrefix(*entry.Candidate, "candidate:") ||
			(entry.SDPMid == nil && entry.SDPMLineIndex == nil) {
			return false
		}
		if (entry.SDPMid != nil && len(*entry.SDPMid) > 64) ||
			(entry.SDPMLineIndex != nil && *entry.SDPMLineIndex > 255) ||
			(entry.UsernameFragment != nil && len(*entry.UsernameFragment) > 256) {
			return false
		}
	}
	return true
}

func isPrivateCallRead(kind netproto.MessageType, body any) bool {
	request, ok := body.(netproto.CallRequest)
	return ok && kind == netproto.MsgCallRequest && (request.Action == "get" || request.Action == "history")
}

func (m *connManager) privateCallRequest(request netproto.CallRequest) (netproto.CallResult, error) {
	frame, err := m.request(netproto.MsgCallRequest, netproto.MsgCallResult, request, 10*time.Second)
	if err != nil {
		return netproto.CallResult{}, err
	}
	var result netproto.CallResult
	if err := netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.Action != request.Action || len(result.History) > 50 {
		return netproto.CallResult{}, errors.New("invalid call response")
	}
	if request.Action != "history" {
		if result.Call == nil || result.Call.ID == "" || result.Call.Revision <= 0 || len(result.Call.Participants) < 2 || len(result.Call.Participants) > netproto.MaxConversationMembers || (request.Action != "start" && result.Call.ID != request.ID) {
			return netproto.CallResult{}, errors.New("invalid call state")
		}
		m.mu.Lock()
		uid, clientID := m.uniqueID, m.clientID
		m.mu.Unlock()
		participant, found := result.Call.Participant(uid)
		if !found || !callParticipantVisible(participant, clientID) {
			return netproto.CallResult{}, errors.New("call belongs to another session")
		}
	}
	return result, nil
}

// callParticipantVisible reports whether this session may read the account's
// participation: its own bound participant, a callee still ringing on every
// device (unbound, no ClientID yet), or the server's "elsewhere" projection
// after another device of the account answered (441). Media and signaling
// still require the bound participant (privateCallPeerKey).
func callParticipantVisible(participant netproto.CallParticipant, clientID string) bool {
	if participant.ClientID == clientID {
		return true
	}
	if participant.ClientID == "" && participant.State == "ringing" {
		return true
	}
	return participant.State == netproto.CallStateElsewhere
}

func (a *App) PrivateCallForTab(tabID string, request netproto.CallRequest) (netproto.CallResult, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.CallResult{}, err
	}
	if request.Action == "signal" {
		return netproto.CallResult{}, errors.New("use authenticated call signaling")
	}
	return m.privateCallRequest(request)
}

// Teardown is allowed on the captured old tab after activation changes. This
// endpoint can only stop this session's existing call, never start media there.
func (a *App) StopPrivateCallForTab(tabID, callID string) error {
	a.tabsMu.Lock()
	tab := a.tabs[tabID]
	if tab == nil || tab.cm == nil {
		a.tabsMu.Unlock()
		return nil
	}
	m := tab.cm
	a.tabsMu.Unlock()
	result, err := m.privateCallRequest(netproto.CallRequest{Action: "get", ID: callID})
	if err != nil {
		return err
	}
	if result.Call.EndedAt != 0 {
		return nil
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	participant, found := result.Call.Participant(uid)
	if !found {
		return nil
	}
	action := "leave"
	if participant.State == "ringing" {
		action = "decline"
	} else if participant.State != "accepted" {
		return nil
	}
	_, err = m.privateCallRequest(netproto.CallRequest{Action: action, ID: callID})
	return err
}

func (a *App) SendPrivateCallDescriptionForTab(tabID, callID, target, kind, sdp string) error {
	return a.sendPrivateCallDescription(tabID, callID, target, "", kind, sdp)
}

// SendPrivateCallScreenReplyForTab answers a peer's native screen
// sub-connection (this side views it): answer and candidates.
func (a *App) SendPrivateCallScreenReplyForTab(tabID, callID, target, kind, sdp string) error {
	return a.sendPrivateCallDescription(tabID, callID, target, callSubScreenReply, kind, sdp)
}

func (a *App) sendPrivateCallDescription(tabID, callID, target, sub, kind, sdp string) error {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	description := PrivateCallDescription{Type: kind, SDP: sdp, Sub: sub}
	if sub == "" && (kind == "offer" || kind == "answer") {
		description.Features = callFeatures
	}
	return m.sendPrivateCallDescription(callID, target, description)
}

// sendPrivateCallDescription seals one description to the target's call key
// and relays it. Call signals of the webview and of a native screen sender
// share one pace: the server's signal allowance is per account.
func (m *connManager) sendPrivateCallDescription(callID, target string, description PrivateCallDescription) error {
	if !validPrivateCallPayload(description.Type, description.SDP) || !validCallDescriptionShape(description) {
		return errors.New("invalid call description")
	}
	id, err := m.identity()
	if err != nil {
		return err
	}
	ownPub, priv, err := id.x25519()
	if err != nil {
		return err
	}
	pub, err := m.privateCallPeerKey(callID, target, ownPub)
	if err != nil {
		return err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	description.CallID, description.From, description.To = callID, uid, target
	plain, err := json.Marshal(description)
	if err != nil {
		return err
	}
	sealed, err := sealDM(string(plain), pub, priv)
	if err != nil {
		return err
	}
	m.paceCallSignal()
	_, err = m.privateCallRequest(netproto.CallRequest{Action: "signal", ID: callID, Target: target, Signal: sealed})
	return err
}

func openPrivateCallDescription(signal netproto.CallSignal, uid string, pub, priv [32]byte) (PrivateCallDescription, error) {
	var description PrivateCallDescription
	if len(signal.Body) > 96000 || signal.To != uid {
		return description, errors.New("invalid call signal recipient")
	}
	plain, err := openDM(signal.Body, pub, priv)
	if err != nil {
		return description, err
	}
	if err := json.Unmarshal([]byte(plain), &description); err != nil {
		return description, err
	}
	if description.CallID != signal.CallID || description.From != signal.From || description.To != uid ||
		!validPrivateCallPayload(description.Type, description.SDP) || !validCallDescriptionShape(description) {
		return PrivateCallDescription{}, errors.New("call description authentication failed")
	}
	return description, nil
}

func (a *App) OpenPrivateCallDescriptionForTab(tabID string, signal netproto.CallSignal) (PrivateCallDescription, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return PrivateCallDescription{}, err
	}
	id, err := m.identity()
	if err != nil {
		return PrivateCallDescription{}, err
	}
	ownPub, priv, err := id.x25519()
	if err != nil {
		return PrivateCallDescription{}, err
	}
	pub, err := m.privateCallPeerKey(signal.CallID, signal.From, ownPub)
	if err != nil {
		return PrivateCallDescription{}, err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	description, err := openPrivateCallDescription(signal, uid, pub, priv)
	if err == nil && description.Sub == callSubScreenReply {
		// The viewer's reply belongs to this side's native screen sender.
		m.routeCallScreenReply(description)
	}
	return description, err
}

// callSignalSpacing keeps call signals of one session under the server's
// per-account allowance (32/s) even when the webview and Go both signal.
const callSignalSpacing = 55 * time.Millisecond

func (m *connManager) paceCallSignal() {
	m.callSignalMu.Lock()
	defer m.callSignalMu.Unlock()
	if wait := time.Until(m.callSignalNext); wait > 0 {
		time.Sleep(wait)
	}
	m.callSignalNext = time.Now().Add(callSignalSpacing)
}

// Call membership pins one device per account. Resolve its immutable key from
// the authenticated call snapshot, never the account-wide DM directory cache.
func (m *connManager) privateCallPeerKey(callID, target string, ownPub [32]byte) ([32]byte, error) {
	result, err := m.privateCallRequest(netproto.CallRequest{Action: "get", ID: callID})
	if err != nil {
		return [32]byte{}, err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	call := result.Call
	me, _ := call.Participant(uid) // privateCallRequest checked this session.
	peer, found := call.Participant(target)
	if call.EndedAt != 0 || me.State != "accepted" || !found || peer.State != "accepted" || target == uid {
		return [32]byte{}, errors.New("call participant is no longer active")
	}
	bound := call.KeyBinding
	for _, participant := range call.Participants {
		bound = bound || participant.PublicKey != ""
	}
	if !bound {
		key, found := m.peerPubKey(target)
		if !found {
			return key, errors.New("call peer encryption key unavailable")
		}
		return key, nil
	}
	if !call.KeyBinding {
		return [32]byte{}, errors.New("invalid call device key binding")
	}
	for _, participant := range call.Participants {
		if participant.State != "accepted" {
			continue // a callee ringing on several devices binds on answer
		}
		_, recordErr := e2ee.ParseKeyRecord(participant.PublicKey, participant.UniqueID)
		_, deviceKey := parseDeviceX25519(participant.PublicKey)
		if participant.ClientID == "" || (recordErr != nil && (participant.DeviceID == "" || !deviceKey)) {
			return [32]byte{}, errors.New("invalid call participant encryption key")
		}
	}
	localKey, ownErr := m.verifyMessageKey(uid, me.PublicKey, uid, ownPub)
	if ownErr != nil || localKey != ownPub {
		return [32]byte{}, errors.New("call belongs to another encryption device")
	}
	if _, err := e2ee.ParseKeyRecord(peer.PublicKey, target); err != nil && peer.DeviceID != "" {
		return m.verifyCallDeviceKey(target, peer.DeviceID, peer.PublicKey)
	}
	return m.verifyPeerRecord(target, peer.PublicKey, true)
}

// verifyCallDeviceKey accepts a device-bound call key (a phone answered)
// only when the device is in the peer's trusted device list.
func (m *connManager) verifyCallDeviceKey(uid, deviceID, encoded string) ([32]byte, error) {
	key, ok := parseDeviceX25519(encoded)
	if !ok {
		return [32]byte{}, errors.New("invalid call participant encryption key")
	}
	for _, force := range []bool{false, true} {
		devices, err := m.dmDeviceList(uid, force)
		if err != nil {
			return [32]byte{}, err
		}
		for _, d := range devices {
			if d.DeviceID == deviceID && d.X25519 == key {
				return key, nil
			}
		}
	}
	return [32]byte{}, errors.New("the answering device is not confirmed for this contact")
}
