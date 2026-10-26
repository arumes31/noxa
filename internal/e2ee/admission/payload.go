package admission

import (
	"bytes"
	"encoding/json"
	"noxa/internal/e2ee/ratchet"
	"sort"
)

type PayloadContext struct {
	Version         string `json:"version"`
	Server          string `json:"server"`
	Sender          string `json:"sender"`
	Device          string `json:"device"`
	AccountEpoch    int64  `json:"account_epoch"`
	AttemptID       string `json:"attempt_id"`
	LogicalID       string `json:"logical_id"`
	Purpose         string `json:"purpose"`
	Reference       string `json:"reference"`
	Scope           string `json:"scope"`
	Peer            string `json:"peer"`
	Conversation    string `json:"conversation"`
	Revision        int64  `json:"revision"`
	Epoch           int64  `json:"epoch"`
	TargetPrincipal string `json:"target_principal"`
	TargetDevice    string `json:"target_device"`
	TargetEpoch     int64  `json:"target_epoch"`
}

func Context(m Manifest, t Target) PayloadContext {
	return PayloadContext{Version, m.Server, m.Sender, m.Device, m.AccountEpoch, m.AttemptID, m.LogicalID, m.Purpose, m.Reference, m.Scope, m.Peer, m.Conversation, m.Revision, m.Epoch, t.Principal, t.Device, t.AccountEpoch}
}
func SealPayload(m Manifest, t Target, body json.RawMessage) (string, error) {
	b, e := json.Marshal(struct {
		Context PayloadContext  `json:"context"`
		Body    json.RawMessage `json:"body"`
	}{Context(m, t), body})
	return string(b), e
}
func OpenPayload(m Manifest, t Target, plaintext string) (json.RawMessage, error) {
	var p struct {
		Context PayloadContext  `json:"context"`
		Body    json.RawMessage `json:"body"`
	}
	if Decode([]byte(plaintext), &p, ratchet.MaxPlaintext) != nil || p.Context != Context(m, t) || len(p.Body) == 0 {
		return nil, ErrInvalid
	}
	return p.Body, nil
}
func ExpectedBinding(m Manifest, t Target) ratchet.Binding {
	conversation := "group:" + m.Conversation
	if m.Scope == "direct" {
		ps := []string{m.Sender, m.Peer}
		sort.Strings(ps)
		b, _ := json.Marshal(ps)
		conversation = "direct:" + string(b)
	}
	purpose := m.Purpose
	if purpose == "chat" {
		purpose = "message"
	}
	return ratchet.Binding{Server: m.Server, Conversation: conversation, Sender: m.Sender, SenderDevice: m.Device, Recipient: t.Principal, RecipientDevice: t.Device, MessageID: m.LogicalID, Purpose: purpose}
}

// CheckEnvelope checks routable public bindings; authenticated inner payload
// verification remains mandatory after the core successfully decrypts.
func CheckEnvelope(m Manifest, c Copy) error {
	var e ratchet.Envelope
	if Decode(c.Envelope, &e, MaxEnvelopeBytes) != nil || e.Version != ratchet.Version || e.SessionID != c.SessionID {
		return ErrInvalid
	}
	t := Target{Principal: c.Principal, Device: c.Device, AccountEpoch: c.AccountEpoch}
	if e.Binding != ExpectedBinding(m, t) || len(bytes.TrimSpace(e.Message)) < 2 {
		return ErrInvalid
	}
	return nil
}
