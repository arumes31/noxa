package netproto

import (
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
)

const (
	MsgOlmAdmissionRequest MessageType = 202
	MsgOlmAdmissionResult  MessageType = 203
)

type OlmAdmissionRequest struct {
	RequestID    string              `json:"request_id"`
	Action       string              `json:"action"`
	AccountEpoch int64               `json:"account_epoch"`
	AttemptID    string              `json:"attempt_id,omitempty"`
	Manifest     *admission.Manifest `json:"manifest,omitempty"`
	Part         int                 `json:"part,omitempty"`
	Data         []byte              `json:"data,omitempty"`
	Scope        string              `json:"scope,omitempty"`
	Peer         string              `json:"peer,omitempty"`
	Conversation string              `json:"conversation,omitempty"`
	DeliveryID   string              `json:"delivery_id,omitempty"`
	Digest       string              `json:"digest,omitempty"`
	After        string              `json:"after,omitempty"`
	Keys         []string            `json:"keys,omitempty"`
	Reset        *olmdir.Reset       `json:"reset,omitempty"`
	Bundle       *olmdir.Bundle      `json:"bundle,omitempty"`
	TargetDevice string              `json:"target_device,omitempty"`
	AfterEpoch   int64               `json:"after_epoch,omitempty"`
}
type OlmAdmissionResult struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
	Data      any    `json:"data,omitempty"`
}

func DecodeOlmAdmissionRequest(f *Frame) (OlmAdmissionRequest, error) {
	var r OlmAdmissionRequest
	if f == nil {
		return r, admission.ErrInvalid
	}
	if e := admission.Decode(f.Payload, &r, 768<<10); e != nil {
		return r, e
	}
	if !olmdir.ValidText(r.RequestID) || r.AccountEpoch < 1 || r.AccountEpoch > 9007199254740991 {
		return r, admission.ErrInvalid
	}
	switch r.Action {
	case "begin", "abort":
		if r.Manifest == nil || r.Manifest.Validate() != nil {
			return r, admission.ErrInvalid
		}
	case "put_part":
		if _, e := admission.AttemptSequence(r.AttemptID); e != nil || len(r.Data) > admission.MaxPartBytes || r.Part < 0 || r.Part >= 8 {
			return r, admission.ErrInvalid
		}
	case "finalize", "status", "retire_receipt":
		if _, e := admission.AttemptSequence(r.AttemptID); e != nil {
			return r, e
		}
	case "snapshot":
		if r.Scope != "direct" && r.Scope != "group" {
			return r, admission.ErrInvalid
		}
	case "receive_ack":
		if !olmdir.ValidText(r.DeliveryID) || len(r.Digest) != 64 {
			return r, admission.ErrInvalid
		}
	case "deliveries":
	case "close_keys":
		if len(r.Keys) < 1 || len(r.Keys) > 100 {
			return r, admission.ErrInvalid
		}
	case "reset":
		if r.Reset == nil || r.Bundle == nil {
			return r, admission.ErrInvalid
		}
	case "reset_chain":
		if !olmdir.ValidText(r.TargetDevice) || r.AfterEpoch < 1 {
			return r, admission.ErrInvalid
		}
	default:
		return r, admission.ErrInvalid
	}
	return r, nil
}
