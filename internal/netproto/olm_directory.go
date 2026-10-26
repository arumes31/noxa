package netproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"noxa/internal/e2ee/olmdir"
)

const (
	MsgOlmDirectoryRequest MessageType = 200
	MsgOlmDirectoryResult  MessageType = 201
	CapabilityOlmDirectory             = olmdir.Version
)

type OlmDirectoryRequest struct {
	RequestID  string         `json:"request_id"`
	ClaimUntil int64          `json:"claim_until,omitempty"`
	Action     string         `json:"action"`
	Principal  string         `json:"principal,omitempty"`
	DeviceID   string         `json:"device_id,omitempty"`
	Bundle     *olmdir.Bundle `json:"bundle,omitempty"`
}
type OlmDirectoryResult struct {
	RequestID  string         `json:"request_id"`
	ClaimUntil int64          `json:"claim_until,omitempty"`
	Action     string         `json:"action"`
	Error      string         `json:"error,omitempty"`
	Bundle     *olmdir.Bundle `json:"bundle,omitempty"`
	KeyID      string         `json:"key_id,omitempty"`
}

// DecodeOlmDirectoryRequest rejects duplicate fields, unknown fields, nesting
// abuse and trailing JSON before interpreting authority or signed data.
func DecodeOlmDirectoryRequest(f *Frame) (OlmDirectoryRequest, error) {
	var r OlmDirectoryRequest
	if f == nil || len(f.Payload) > olmdir.MaxBytes {
		return r, olmdir.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(f.Payload))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 8 {
			return olmdir.ErrInvalid
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				if !ok || seen[k] {
					return olmdir.ErrInvalid
				}
				seen[k] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return olmdir.ErrInvalid
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return r, e
	}
	if _, e := d.Token(); !errors.Is(e, io.EOF) {
		return r, olmdir.ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(f.Payload))
	d.DisallowUnknownFields()
	if e := d.Decode(&r); e != nil {
		return r, e
	}
	if !olmdir.ValidText(r.RequestID) {
		return r, olmdir.ErrInvalid
	}
	switch r.Action {
	case "publish":
		if r.ClaimUntil != 0 || r.Bundle == nil || r.Principal != "" || r.DeviceID != "" {
			return r, olmdir.ErrInvalid
		}
	case "claim":
		if r.ClaimUntil < 1 || r.ClaimUntil > 9007199254740991 || r.Bundle != nil || !olmdir.ValidText(r.Principal) || !olmdir.ValidText(r.DeviceID) {
			return r, olmdir.ErrInvalid
		}
	default:
		return r, olmdir.ErrInvalid
	}
	return r, nil
}
