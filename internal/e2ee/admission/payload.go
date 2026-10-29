package admission

import (
	"bytes"
	"encoding/json"
	"io"
	"noxa/internal/e2ee/ratchet"
	"sort"
	"strings"
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
	// Decode only the two outer fields here. Application JSON is opaque;
	// strict wire grammar applies exclusively to authenticated context.
	if len(plaintext) == 0 || len(plaintext) > ratchet.MaxPlaintext {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(plaintext))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrInvalid
		}
		switch key {
		case "context":
			if Decode(raw, &p.Context, ratchet.MaxPlaintext) != nil {
				return nil, ErrInvalid
			}
		case "body":
			p.Body = raw
		default:
			return nil, ErrInvalid
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	if !seen["context"] || p.Context != Context(m, t) || !json.Valid(p.Body) {
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
	switch purpose {
	case "chat":
		purpose = "message"
	case "control":
		purpose = "media-key"
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
