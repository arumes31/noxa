package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"noxa/internal/netproto"
)

type PrivateCallDescription struct {
	CallID string `json:"call_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Type   string `json:"type"`
	SDP    string `json:"sdp"`
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
		if !found || participant.ClientID != clientID {
			return netproto.CallResult{}, errors.New("call belongs to another session")
		}
	}
	return result, nil
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
	if !validPrivateCallPayload(kind, sdp) {
		return errors.New("invalid call description")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	id, err := m.identity()
	if err != nil {
		return err
	}
	_, priv, err := id.x25519()
	if err != nil {
		return err
	}
	pub, found := m.peerPubKey(target)
	if !found {
		return errors.New("call peer encryption key unavailable")
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	plain, err := json.Marshal(PrivateCallDescription{CallID: callID, From: uid, To: target, Type: kind, SDP: sdp})
	if err != nil {
		return err
	}
	sealed, err := sealDM(string(plain), pub, priv)
	if err != nil {
		return err
	}
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
		!validPrivateCallPayload(description.Type, description.SDP) {
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
	_, priv, err := id.x25519()
	if err != nil {
		return PrivateCallDescription{}, err
	}
	pub, found := m.peerPubKey(signal.From)
	if !found {
		return PrivateCallDescription{}, errors.New("call sender encryption key unavailable")
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	return openPrivateCallDescription(signal, uid, pub, priv)
}
